// Package config defines the palm-gw configuration schema (PLAN.md section 7),
// its defaults, the TOML loader and the validation rules. Every key has a
// default, so an empty file is a valid configuration.
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// DefaultPath is where the daemon looks for its configuration.
const DefaultPath = "/etc/palm-gw/config.toml"

// Config is the whole /etc/palm-gw/config.toml. JSON tags mirror the TOML keys
// so the control socket can echo the configuration with the same names.
type Config struct {
	Bluetooth Bluetooth `toml:"bluetooth" json:"bluetooth"`
	SPP       Profile   `toml:"spp" json:"spp"`
	LAP       Profile   `toml:"lap" json:"lap"`
	DUN       Profile   `toml:"dun" json:"dun"`
	PAN       PAN       `toml:"pan" json:"pan"`
	Handshake Handshake `toml:"handshake" json:"handshake"`
	Network   Network   `toml:"network" json:"network"`
	PPP       PPP       `toml:"ppp" json:"ppp"`
	Firewall  Firewall  `toml:"firewall" json:"firewall"`
	Logging   Logging   `toml:"logging" json:"logging"`
	Ctl       Ctl       `toml:"ctl" json:"ctl"`
	Web       Web       `toml:"web" json:"web"`
}

// Bluetooth is the [bluetooth] table.
type Bluetooth struct {
	Adapter                  string   `toml:"adapter" json:"adapter"`
	Name                     string   `toml:"name" json:"name"`
	Class                    uint32   `toml:"class" json:"class"`
	ClassFallback            uint32   `toml:"class_fallback" json:"class_fallback"`
	PIN                      string   `toml:"pin" json:"pin"`
	PINLength                int      `toml:"pin_length" json:"pin_length"`
	PairWindowDefaultMinutes int      `toml:"pair_window_default_minutes" json:"pair_window_default_minutes"`
	AllowAny                 bool     `toml:"allow_any" json:"allow_any"`
	Allow                    []string `toml:"allow" json:"allow"`
	TrustOnPair              bool     `toml:"trust_on_pair" json:"trust_on_pair"`
}

// Profile is one of the [spp], [lap], [dun] tables.
type Profile struct {
	Enabled bool `toml:"enabled" json:"enabled"`
	Channel int  `toml:"channel" json:"channel"`
}

// PAN is the [pan] table.
type PAN struct {
	Enabled bool   `toml:"enabled" json:"enabled"`
	Bridge  string `toml:"bridge" json:"bridge"`
}

// Handshake is the [handshake] table (PLAN.md section 8.4).
type Handshake struct {
	MSNullModem     bool   `toml:"ms_null_modem" json:"ms_null_modem"`
	MSTimeoutMS     int    `toml:"ms_timeout_ms" json:"ms_timeout_ms"`
	ATEmulator      bool   `toml:"at_emulator" json:"at_emulator"`
	ATConnectBanner string `toml:"at_connect_banner" json:"at_connect_banner"`
	ATTimeoutMS     int    `toml:"at_timeout_ms" json:"at_timeout_ms"`
}

// Network is the [network] table.
type Network struct {
	Subnet      string   `toml:"subnet" json:"subnet"`
	Gateway     string   `toml:"gateway" json:"gateway"`
	PPPPool     []string `toml:"ppp_pool" json:"ppp_pool"`
	DHCPPool    []string `toml:"dhcp_pool" json:"dhcp_pool"`
	Uplink      string   `toml:"uplink" json:"uplink"`
	DNSUpstream []string `toml:"dns_upstream" json:"dns_upstream"`
	MSSClamp    bool     `toml:"mss_clamp" json:"mss_clamp"`
}

// PPP is the [ppp] table.
type PPP struct {
	OptionsFile  string `toml:"options_file" json:"options_file"`
	Debug        bool   `toml:"debug" json:"debug"`
	IdleSeconds  int    `toml:"idle_seconds" json:"idle_seconds"`
	MaxSessions  int    `toml:"max_sessions" json:"max_sessions"`
	PerDeviceMax int    `toml:"per_device_max" json:"per_device_max"`
}

// Firewall is the [firewall] table.
type Firewall struct {
	ManageNftables bool        `toml:"manage_nftables" json:"manage_nftables"`
	PalmToPi       []string    `toml:"palm_to_pi" json:"palm_to_pi"`
	Allow          []AllowRule `toml:"allow" json:"allow"`
}

// AllowRule is one [[firewall.allow]] entry: a LAN host (IP or CIDR) and the
// proto/port tuples Palms may reach on it.
type AllowRule struct {
	Host    string   `toml:"host" json:"host"`
	Ports   []string `toml:"ports" json:"ports"`
	Comment string   `toml:"comment" json:"comment"`
}

// Logging is the [logging] table.
type Logging struct {
	Level          string `toml:"level" json:"level"`
	Format         string `toml:"format" json:"format"`
	BtmonOnFailure bool   `toml:"btmon_on_failure" json:"btmon_on_failure"`
}

// Ctl is the [ctl] table.
type Ctl struct {
	Socket string `toml:"socket" json:"socket"`
	Group  string `toml:"group" json:"group"`
}

// Web is the [web] table.
type Web struct {
	Enabled      bool     `toml:"enabled" json:"enabled"`
	Username     string   `toml:"username" json:"username"` // D-026
	Listen       []string `toml:"listen" json:"listen"`
	TLS          string   `toml:"tls" json:"tls"`
	TLSCertFile  string   `toml:"tls_cert_file" json:"tls_cert_file"`
	TLSKeyFile   string   `toml:"tls_key_file" json:"tls_key_file"`
	PasswordHash string   `toml:"password_hash" json:"password_hash"`
	SessionHours int      `toml:"session_hours" json:"session_hours"`
	AllowCIDRs   []string `toml:"allow_cidrs" json:"allow_cidrs"` // the LAN listener only

	// The Bluetooth-side listener on the gateway address, TLS only (D-026, D-039).
	BluetoothSide bool `toml:"bluetooth_side" json:"bluetooth_side"`
	// The optional read-only account (docs/WEB-UI.md section 3, D-049).
	ViewerUsername     string `toml:"viewer_username" json:"viewer_username"`
	ViewerPasswordHash string `toml:"viewer_password_hash" json:"viewer_password_hash"`
}

// Default returns the configuration that an empty file yields (PLAN.md section 7).
func Default() *Config {
	return &Config{
		Bluetooth: Bluetooth{
			Adapter:                  "hci0",
			Name:                     "Palm Gateway",
			Class:                    0x000300,
			ClassFallback:            0x000104,
			PIN:                      "",
			PINLength:                4,
			PairWindowDefaultMinutes: 5,
			AllowAny:                 false,
			Allow:                    []string{},
			TrustOnPair:              true,
		},
		SPP: Profile{Enabled: true, Channel: 1},
		LAP: Profile{Enabled: true, Channel: 2},
		DUN: Profile{Enabled: true, Channel: 3},
		PAN: PAN{Enabled: true, Bridge: "br-palm"},
		Handshake: Handshake{
			MSNullModem:     true,
			MSTimeoutMS:     10000, // a Pocket PC can take over 3 s to send CLIENT (D-060)
			ATEmulator:      true,
			ATConnectBanner: "CONNECT 115200",
			ATTimeoutMS:     10000,
		},
		Network: Network{
			Subnet:      "10.42.0.0/24",
			Gateway:     "10.42.0.1",
			PPPPool:     []string{"10.42.0.10", "10.42.0.99"},
			DHCPPool:    []string{"10.42.0.100", "10.42.0.199"},
			Uplink:      "eth0",
			DNSUpstream: []string{},
			MSSClamp:    true,
		},
		PPP: PPP{
			OptionsFile:  "/etc/ppp/options.palm",
			Debug:        false,
			IdleSeconds:  0,
			MaxSessions:  8,
			PerDeviceMax: 1,
		},
		Firewall: Firewall{
			ManageNftables: true,
			PalmToPi:       []string{"udp/53", "udp/67", "icmp"},
			Allow:          []AllowRule{},
		},
		Logging: Logging{Level: "info", Format: "json", BtmonOnFailure: false},
		Ctl:     Ctl{Socket: "/run/palm-gw/ctl.sock", Group: "palm-gw"},
		Web: Web{
			Enabled:       true,
			Username:      "admin",
			Listen:        []string{"0.0.0.0:8042"},
			TLS:           "self-signed",
			SessionHours:  72,
			AllowCIDRs:    []string{},
			BluetoothSide: true,
		},
	}
}

// Load reads, decodes and validates the TOML file at path. Keys that are not
// part of the schema are an error, so typos cannot silently fall back to a
// default. The returned error names the offending key.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg, err := Parse(string(data))
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Parse decodes TOML text over the defaults without validating it.
func Parse(text string) (*Config, error) {
	cfg := Default()
	md, err := toml.Decode(text, cfg)
	if err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			return nil, fmt.Errorf("config: %s", perr.ErrorWithPosition())
		}
		return nil, fmt.Errorf("config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		var errs Errors
		for _, k := range undecoded {
			errs = append(errs, Error{Key: k.String(), Msg: "unknown key"})
		}
		return nil, errs
	}
	return cfg, nil
}

// Redacted returns a copy with secrets replaced, for echoing over the control
// socket and into support bundles.
func (c *Config) Redacted() *Config {
	cp := *c
	if cp.Bluetooth.PIN != "" {
		cp.Bluetooth.PIN = "(redacted)"
	}
	if cp.Web.PasswordHash != "" {
		cp.Web.PasswordHash = "(redacted)"
	}
	if cp.Web.ViewerPasswordHash != "" {
		cp.Web.ViewerPasswordHash = "(redacted)"
	}
	return &cp
}
