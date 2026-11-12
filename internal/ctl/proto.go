// Package ctl is the daemon's control plane: a Unix domain socket speaking
// newline-delimited JSON (PLAN.md section 8.8), the server side used by
// palm-gwd, the client side used by palm-gwctl and the web UI, and the CLI.
//
// Protocol: the client opens a connection, writes exactly one Request as a
// JSON line, and reads JSON lines until the server closes the connection.
// Most commands answer with one line. Streaming commands (pair, events) keep
// writing lines until the client disconnects, which cancels the command.
// An error is a line {"error":"..."}.
package ctl

import (
	"encoding/json"
	"time"

	"palm-gw/internal/agent"
	"palm-gw/internal/allow"
	"palm-gw/internal/bluez"
	"palm-gw/internal/config"
	"palm-gw/internal/fw"
	"palm-gw/internal/ppp"
	"palm-gw/internal/profile"
	"palm-gw/internal/setup"
)

// DefaultSocket is the control socket path when none is configured.
const DefaultSocket = "/run/palm-gw/ctl.sock"

// Request is the one JSON object a client sends. Only Cmd is always present;
// the other fields belong to particular commands.
type Request struct {
	Cmd     string          `json:"cmd"`
	Minutes int             `json:"minutes,omitempty"` // pair
	One     bool            `json:"one,omitempty"`     // pair: close after one device
	Addr    string          `json:"addr,omitempty"`    // disconnect, forget, allow, rename
	Name    string          `json:"name,omitempty"`    // allow: optional device name
	Label   string          `json:"label,omitempty"`   // rename
	Seconds int             `json:"seconds,omitempty"` // btmon
	Recent  int             `json:"recent,omitempty"`  // events: send up to this many past events first
	Since   uint64          `json:"since,omitempty"`   // events: send the past events after this seq first
	NoWait  bool            `json:"no_wait,omitempty"` // events: send only the past events, then end
	Path    string          `json:"path,omitempty"`    // config.get / config.set
	Value   json.RawMessage `json:"value,omitempty"`   // config.set
	Host    string          `json:"host,omitempty"`    // firewall.test
	Port    int             `json:"port,omitempty"`    // firewall.test
	// network.set: the settings only `palm-gw setup --apply` can apply (D-054).
	Network *setup.NetworkChange `json:"network,omitempty"`
}

// ErrorReply is the line the server writes when a command fails.
type ErrorReply struct {
	Error string `json:"error"`
}

// OKReply is the line for commands with nothing else to say.
type OKReply struct {
	OK bool `json:"ok"`
}

// BlueZStatus is the daemon's view of bluetoothd.
type BlueZStatus struct {
	Present         bool      `json:"present"`
	AgentRegistered bool      `json:"agent_registered"`
	Since           time.Time `json:"since,omitempty"`
	Losses          int       `json:"losses"` // how many times org.bluez vanished since start
}

// AdapterStatus is the selected controller.
type AdapterStatus struct {
	HCI       string `json:"hci"`
	Path      string `json:"path"`
	Selection string `json:"selection"` // why this adapter: configured, auto: ...
	HWBus     string `json:"hw_bus"`    // "usb", "serial", ... from sysfs
	bluez.AdapterProps
}

// StatusReply is the answer to {"cmd":"status"} (PLAN.md section 8.8).
type StatusReply struct {
	Version       string             `json:"version"`
	Phase         string             `json:"phase"`
	StartedAt     time.Time          `json:"started_at"`
	UptimeSeconds int64              `json:"uptime_seconds"`
	ConfigPath    string             `json:"config_path"`
	StatePath     string             `json:"state_path"`
	Warnings      []string           `json:"warnings,omitempty"`
	BlueZ         BlueZStatus        `json:"bluez"`
	Adapter       *AdapterStatus     `json:"adapter,omitempty"`
	PairingWindow agent.State        `json:"pairing_window"`
	Allowlist     []allow.Entry      `json:"allowlist"`
	AllowAny      bool               `json:"allow_any"`
	Devices       []bluez.DeviceInfo `json:"devices"` // every device BlueZ knows under the adapter
	Profiles      []profile.Status   `json:"profiles"`
	Sessions      []ppp.Info         `json:"sessions"`
	PoolInUse     int                `json:"ppp_pool_in_use"`
	Firewall      FirewallStatus     `json:"firewall"`
	PAN           PANStatus          `json:"pan"`
	Config        *config.Config     `json:"config"`
}

// FirewallStatus is the state of the daemon-managed nftables sets.
type FirewallStatus struct {
	Managed    bool      `json:"managed"`              // [firewall].manage_nftables
	AllowRules int       `json:"allow_rules"`          // [[firewall.allow]] blocks
	LANPorts   int       `json:"lan_ports"`            // proto/port entries across them
	LANICMP    int       `json:"lan_icmp"`             // "icmp" entries across them
	PalmToPi   int       `json:"palm_to_pi"`           // firewall.palm_to_pi entries
	AppliedAt  time.Time `json:"applied_at,omitempty"` // last successful apply
	Error      string    `json:"error,omitempty"`      // last apply failed with this
}

// PANStatus is the NAP server and its clients (PLAN.md section 8.6).
type PANStatus struct {
	Enabled    bool        `json:"enabled"`
	Bridge     string      `json:"bridge"`
	Registered bool        `json:"registered"`
	Since      time.Time   `json:"since,omitempty"`
	Error      string      `json:"error,omitempty"`
	Ports      []string    `json:"ports"` // interfaces enslaved to the bridge
	Clients    []PANClient `json:"clients"`
}

// PANClient is one connected PANU.
type PANClient struct {
	Addr     string    `json:"addr"`
	Name     string    `json:"name"`
	Ifname   string    `json:"ifname"`
	IP       string    `json:"ip,omitempty"` // from the dnsmasq lease, once there is one
	Hostname string    `json:"hostname,omitempty"`
	Since    time.Time `json:"since"`
}

// ReloadReply reports what `reload` changed (D-030).
type ReloadReply struct {
	OK               bool     `json:"ok"`
	Applied          []string `json:"applied"`       // keys now in effect
	NeedsRestart     []string `json:"needs_restart"` // changed keys that take effect when palm-gwd restarts
	DnsmasqRestarted bool     `json:"dnsmasq_restarted"`
	Errors           []string `json:"errors,omitempty"`
}

// PairEvent is one line of the `pair` stream (PLAN.md section 8.8).
type PairEvent struct {
	Event   string    `json:"event"`
	Time    time.Time `json:"time"`
	PIN     string    `json:"pin,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
	Addr    string    `json:"addr,omitempty"`
	Name    string    `json:"name,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Passkey string    `json:"passkey,omitempty"`
	UUID    string    `json:"uuid,omitempty"`
}

// ForgetReply reports what `forget` found and removed.
type ForgetReply struct {
	OK            bool   `json:"ok"`
	Address       string `json:"address"`
	RemovedAllow  bool   `json:"removed_from_allowlist"`
	StaticEntry   bool   `json:"static_entry"` // still allowed by [bluetooth].allow in config
	RemovedBlueZ  bool   `json:"removed_from_bluez"`
	KilledSession bool   `json:"killed_session"`
	DroppedPAN    bool   `json:"dropped_pan"`
}

// DisconnectReply reports what `disconnect` ended.
type DisconnectReply struct {
	OK       bool   `json:"ok"`
	Address  string `json:"address"`
	Sessions int    `json:"sessions"` // how many sessions were terminated
	PAN      bool   `json:"pan"`      // a PAN connection was dropped
}

// AllowReply reports what `allow` did.
type AllowReply struct {
	OK      bool   `json:"ok"`
	Address string `json:"address"`
	Added   bool   `json:"added"` // false if it was already there
}

// RenameReply reports a new operator label (empty clears it).
type RenameReply struct {
	OK      bool   `json:"ok"`
	Address string `json:"address"`
	Label   string `json:"label"`
}

// PairCloseReply reports whether a pairing window was open to close.
type PairCloseReply struct {
	OK      bool `json:"ok"`
	WasOpen bool `json:"was_open"`
}

// ConfigValue is the answer to config.get: the value at path (secrets
// redacted), or the whole configuration for an empty path.
type ConfigValue struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// ConfigSetReply reports a config.set: the file was written and reloaded.
type ConfigSetReply struct {
	OK     bool        `json:"ok"`
	Path   string      `json:"path"`
	Reload ReloadReply `json:"reload"`
}

// FirewallCounter is one counted rule in the palm_gw table.
type FirewallCounter struct {
	Chain   string `json:"chain"`
	Comment string `json:"comment"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// CountersReply is the answer to firewall.counters.
type CountersReply struct {
	Counters []FirewallCounter `json:"counters"`
}

// TestReply is the answer to firewall.test: a TCP connect from the Pi.
type TestReply struct {
	OK      bool   `json:"ok"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Millis  int64  `json:"millis"`
	Problem string `json:"problem,omitempty"` // why the connection failed ("error" would read as a failed command)
	Allowed bool   `json:"allowed"`           // a [[firewall.allow]] rule lets Palms reach it
}

// ChoicesReply is the answer to choices: what the settings page's drop-downs
// offer, and how the last network apply went (D-054).
type ChoicesReply struct {
	Adapters  []setup.Adapter    `json:"adapters"`         // what BlueZ sees now
	AdapterIn string             `json:"adapter_in_use"`   // the address of the one in use, "" if none
	Uplinks   []setup.Uplink     `json:"uplinks"`          // interfaces with a default route
	Subnet    *setup.Subnet      `json:"subnet,omitempty"` // the Palm network checked against the host's
	Applying  bool               `json:"applying"`         // a network apply is running now
	LastApply *setup.ApplyRecord `json:"last_apply,omitempty"`
}

// NetworkSetReply reports a network.set: config.toml was written and
// `palm-gw setup --apply` started, which restarts palm-gwd when it is done.
type NetworkSetReply struct {
	OK       bool     `json:"ok"`
	Changed  []string `json:"changed"`  // section.key, empty when nothing changed
	Applying bool     `json:"applying"` // false when nothing changed
	Unit     string   `json:"unit,omitempty"`
}

// NATEntry is one tracked connection a Palm started, with the device it
// came from when a session or PAN lease has that address.
type NATEntry struct {
	fw.Conn
	Device string `json:"device,omitempty"` // device name, or its Bluetooth address
}

// NATReply is the answer to nat: the kernel's connection tracking entries
// from the Palm network (D-055).
type NATReply struct {
	Subnet     string     `json:"subnet"`
	Entries    []NATEntry `json:"entries"`
	Total      int        `json:"total"`      // every tracked connection on the Pi, Palm or not
	Accounting bool       `json:"accounting"` // nf_conntrack_acct is on, so bytes and packets are counted
}
