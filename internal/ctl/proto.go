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
	Path    string          `json:"path,omitempty"`    // config.get / config.set
	Value   json.RawMessage `json:"value,omitempty"`   // config.set
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
	bluez.AdapterProps
}

// StatusReply is the answer to {"cmd":"status"} (PLAN.md section 8.8).
// Sessions and PAN clients arrive in Phases 2 and 4.
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
	Config        *config.Config     `json:"config"`
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
}

// AllowReply reports what `allow` did.
type AllowReply struct {
	OK      bool   `json:"ok"`
	Address string `json:"address"`
	Added   bool   `json:"added"` // false if it was already there
}
