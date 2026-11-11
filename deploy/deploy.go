// Package deploy embeds the system files palm-gw installs (PLAN.md section 6).
// Two kinds of template live here:
//   - layout templates ({{BIN_DIR}}, {{LIB_DIR}}): the units and the static
//     files, which differ only between a source install and the .deb and are
//     written by `palm-gw setup --install-files` or packed by `make deb`;
//   - configuration templates ({{UPLINK}}, {{SUBNET}}, ...): rendered from
//     config.toml by `palm-gw setup --apply`, and at run time by palm-gwd
//     (`palm-gwctl reload` rewrites the dnsmasq configuration, section 8.8).
package deploy

import "embed"

// FS holds every deploy file under its deploy/ path.
//
//go:embed systemd/*.service lib/nm-bridge.sh NetworkManager/conf.d/palm-gw.conf
//go:embed modules-load.d/palm-gw.conf ppp/options.palm sysctl.d/90-palm-gw.conf
//go:embed dnsmasq/palm-gw.conf.tmpl nftables/palm-gw.nft.tmpl
var FS embed.FS

// Paths of the files inside FS.
const (
	Dnsmasq      = "dnsmasq/palm-gw.conf.tmpl"
	Nftables     = "nftables/palm-gw.nft.tmpl"
	Sysctl       = "sysctl.d/90-palm-gw.conf"
	UnitDaemon   = "systemd/palm-gwd.service"
	UnitBridge   = "systemd/palm-gwd-nm-bridge.service"
	UnitDnsmasq  = "systemd/palm-gw-dnsmasq.service"
	BridgeScript = "lib/nm-bridge.sh"
	NMConf       = "NetworkManager/conf.d/palm-gw.conf"
	ModulesLoad  = "modules-load.d/palm-gw.conf"
	PPPOptions   = "ppp/options.palm"
)
