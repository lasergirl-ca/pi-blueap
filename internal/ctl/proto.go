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

// StatusReply is the answer to {"cmd":"status"}. Phase 0 carries the daemon
// identity and the configuration echo; later phases add adapter, pairing
// window, sessions, PAN clients and allowlist (PLAN.md section 8.8).
type StatusReply struct {
	Version       string         `json:"version"`
	Phase         string         `json:"phase"`
	StartedAt     time.Time      `json:"started_at"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	ConfigPath    string         `json:"config_path"`
	Warnings      []string       `json:"warnings,omitempty"`
	Config        *config.Config `json:"config"`
}
