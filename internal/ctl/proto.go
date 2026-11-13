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
	"palm-gw/internal/devkey"
	"palm-gw/internal/diag"
	"palm-gw/internal/fw"
	"palm-gw/internal/nm"
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
	// wifi.* and internet.use (D-056). The password is never logged.
	WiFi *WifiRequest `json:"wifi,omitempty"`
	// logs (D-057).
	Logs *LogsRequest `json:"logs,omitempty"`
	// bundle: add the newest btmon capture, which can hold link keys.
	WithCapture bool `json:"with_capture,omitempty"`
	// devkey.* (D-063). The key is never logged.
	DevKey *DevKeyRequest `json:"devkey,omitempty"`
	// wg.* (D-069). A .conf carries a private key and is never logged.
	WG *WGRequest `json:"wg,omitempty"`
}

// WGRequest carries the wg.* commands: wg.set changes the settings given,
// wg.import reads Conf.
type WGRequest struct {
	Enabled       *bool    `json:"enabled,omitempty"`
	Mode          string   `json:"mode,omitempty"`    // full or split
	OnDown        string   `json:"on_down,omitempty"` // fail_closed or fallback
	AllowedIPs    []string `json:"allowed_ips,omitempty"`
	Address       string   `json:"address,omitempty"`
	Endpoint      string   `json:"endpoint,omitempty"`
	PeerPublicKey string   `json:"peer_public_key,omitempty"`
	Conf          string   `json:"conf,omitempty"` // wg.import: a wg-quick .conf
}

// WGStatus is the tunnel as status and wg show report it (D-069).
type WGStatus struct {
	Enabled       bool     `json:"enabled"`
	State         string   `json:"state"` // off, up, down, missing (enabled but no profile: apply)
	Interface     string   `json:"interface"`
	Mode          string   `json:"mode"`
	OnDown        string   `json:"on_down"`
	Address       string   `json:"address,omitempty"`
	Endpoint      string   `json:"endpoint,omitempty"`
	PublicKey     string   `json:"public_key,omitempty"` // the gateway's
	PeerPublicKey string   `json:"peer_public_key,omitempty"`
	AllowedIPs    []string `json:"allowed_ips"`
	DNS           []string `json:"dns"`
	RxBytes       uint64   `json:"rx_bytes"`
	TxBytes       uint64   `json:"tx_bytes"`
	// Seconds since the tunnel last received anything; -1 before it has. With a
	// keepalive the peer answers within seconds, so minutes mean it is stale.
	LastHeardSeconds int64 `json:"last_heard_seconds"`
	Stale            bool  `json:"stale"`
	Applying         bool  `json:"applying"`
}

// WGSetReply answers wg.set, wg.import and wg.generate.
type WGSetReply struct {
	OK        bool     `json:"ok"`
	Changed   []string `json:"changed"`
	Applying  bool     `json:"applying"` // palm-gw setup --apply started; palm-gwd restarts
	Unit      string   `json:"unit,omitempty"`
	PublicKey string   `json:"public_key,omitempty"` // the gateway's
	Mode      string   `json:"mode,omitempty"`
}

// DevKeyRequest carries the device key commands.
type DevKeyRequest struct {
	Key  string `json:"key,omitempty"`  // devkey.check
	IP   string `json:"ip,omitempty"`   // devkey.check: where it was used from
	User string `json:"user,omitempty"` // devkey.issue
	Role string `json:"role,omitempty"` // devkey.issue: admin or viewer
	Name string `json:"name,omitempty"` // devkey.issue: the Palm's name
	ID   string `json:"id,omitempty"`   // devkey.revoke
}

// LogsRequest selects journal lines for logs.
type LogsRequest struct {
	Units []string `json:"units,omitempty"` // palm-gwd, bluetooth, dnsmasq, bridge, apply
	Lines int      `json:"lines,omitempty"` // default 500
	After string   `json:"after,omitempty"` // a cursor: only newer lines (Follow)
	Event string   `json:"event,omitempty"` // an event name, or a prefix ending in *
	Text  string   `json:"text,omitempty"`  // a device address or any text
}

// WifiRequest carries the internet wizard's commands.
type WifiRequest struct {
	SSID     string `json:"ssid,omitempty"`     // wifi.join, wifi.forget
	Password string `json:"password,omitempty"` // wifi.join; empty for an open or saved network
	Hidden   bool   `json:"hidden,omitempty"`   // wifi.join: a network that does not broadcast its name
	Rescan   bool   `json:"rescan,omitempty"`   // wifi.scan: ask for a fresh scan first
	On       *bool  `json:"on,omitempty"`       // wifi.radio
	Use      string `json:"use,omitempty"`      // internet.use: ethernet or wifi
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
	WireGuard     *WGStatus          `json:"wireguard,omitempty"`
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

// WifiJob is a Wi-Fi join running in the daemon, which outlives the request
// that started it, so a browser that loses its connection when the Pi
// changes networks does not stop it or its rollback (D-056).
type WifiJob struct {
	SSID      string    `json:"ssid"`
	Started   time.Time `json:"started"`
	Done      bool      `json:"done"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Switching bool      `json:"switching,omitempty"` // the uplink is being switched to Wi-Fi: palm-gwd restarts
}

// InternetReply is the answer to internet: how the Pi reaches the internet.
type InternetReply struct {
	Uplink    string      `json:"uplink"` // [network].uplink
	Devices   []nm.Device `json:"devices"`
	WifiOn    bool        `json:"wifi_on"`
	WifiHW    bool        `json:"wifi_hw"` // false: rfkill hard block
	Saved     []string    `json:"saved"`   // saved Wi-Fi networks
	Job       *WifiJob    `json:"job,omitempty"`
	Applying  bool        `json:"applying"`
	OnboardBT bool        `json:"onboard_bt"`      // the gateway uses the onboard radio, which shares 2.4 GHz with Wi-Fi
	Error     string      `json:"error,omitempty"` // NetworkManager is not reachable
}

// WifiScanReply is the answer to wifi.scan.
type WifiScanReply struct {
	Networks []nm.Network `json:"networks"`
}

// InternetUseReply is the answer to internet.use.
type InternetUseReply struct {
	OK        bool     `json:"ok"`
	Iface     string   `json:"iface"`
	Switching bool     `json:"switching"` // the uplink changed: palm-gw setup --apply runs and palm-gwd restarts
	Changed   []string `json:"changed,omitempty"`
}

// LogsReply is the answer to logs: matching lines, oldest first, and the
// cursor to pass as after for the next lines.
type LogsReply struct {
	Entries []diag.Entry `json:"entries"`
	Cursor  string       `json:"cursor"`
}

// UnitState is a systemd unit's state for check.
type UnitState struct {
	Name   string `json:"name"`
	Active string `json:"active"` // systemctl is-active: active, inactive, failed, ...
}

// ModuleState is whether a kernel module is loaded.
type ModuleState struct {
	Name   string `json:"name"`
	Loaded bool   `json:"loaded"`
}

// CheckReply is the answer to check: palm-gw setup --check's report, with
// the units and kernel modules (PLAN.md section 8.12).
type CheckReply struct {
	setup.Report
	Units   []UnitState   `json:"units"`
	Modules []ModuleState `json:"modules"`
}

// CaptureState is the btmon capture running now, or the last one's error.
type CaptureState struct {
	Running bool      `json:"running"`
	Name    string    `json:"name,omitempty"`
	Started time.Time `json:"started,omitzero"`
	Until   time.Time `json:"until,omitzero"`
	Error   string    `json:"error,omitempty"`
}

// DiagnosticsReply is the answer to diagnostics: the captures and bundles
// on the Pi, newest first, and the capture in progress.
type DiagnosticsReply struct {
	Captures []diag.File  `json:"captures"`
	Bundles  []diag.File  `json:"bundles"`
	Capture  CaptureState `json:"capture"`
}

// BundleReply is the answer to bundle.
type BundleReply struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ClassReply is the answer to class.toggle.
type ClassReply struct {
	Class    string `json:"class"`    // in effect once the apply is done, 0xXXXXXX
	Previous string `json:"previous"` // before
	Applying bool   `json:"applying"`
}

// AboutReply is the answer to about.
type AboutReply struct {
	Version   string             `json:"version"`
	GoVersion string             `json:"go_version"`
	Started   time.Time          `json:"started"`
	Install   *setup.InstallInfo `json:"install,omitempty"`
}

// UpdateReply is the answer to update.check: palm-gw upgrade --check's words.
type UpdateReply struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// DevKeyReply answers devkey.issue (Key set, once) and devkey.check,
// devkey.revoke (Key empty).
type DevKeyReply struct {
	Key   string       `json:"key,omitempty"`
	Entry devkey.Entry `json:"entry"`
}

// DevKeysReply answers devkey.list.
type DevKeysReply struct {
	Keys []devkey.Entry `json:"keys"`
}
