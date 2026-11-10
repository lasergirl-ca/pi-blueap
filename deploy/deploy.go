// Package deploy embeds the templates palm-gwd re-renders at run time
// (`palm-gwctl reload` rewrites the dnsmasq configuration, PLAN.md section
// 8.8). install.sh still renders the files on disk with `palm-gwd -render`.
package deploy

import "embed"

// FS holds the run-time templates, under their deploy/ paths.
//
//go:embed dnsmasq/palm-gw.conf.tmpl nftables/palm-gw.nft.tmpl
var FS embed.FS

// Dnsmasq and Nftables are the paths of the two templates inside FS.
const (
	Dnsmasq  = "dnsmasq/palm-gw.conf.tmpl"
	Nftables = "nftables/palm-gw.nft.tmpl"
)
