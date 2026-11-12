// Package fw fills the daemon-managed nftables sets (PLAN.md section 8.7).
// The table itself is rendered once at install time from
// deploy/nftables/palm-gw.nft.tmpl and loaded by nftables.service; the daemon
// never rewrites it. It only replaces the contents of three sets, atomically,
// with `nft -f -`:
//
//	lan_allow       ipv4_addr . inet_proto . inet_service   [[firewall.allow]] ports
//	lan_allow_icmp  ipv4_addr                               [[firewall.allow]] "icmp"
//	pi_allow        inet_proto . inet_service               firewall.palm_to_pi (D-029)
package fw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"palm-gw/internal/config"
)

// Table is the nftables table the template declares.
const Table = "inet palm_gw"

// Script returns the nft commands that replace the three sets' contents. The
// configuration must already be valid. Hosts are normalised to their network
// address, and a host covered by a wider entry for the same port is dropped,
// because nft refuses overlapping elements in an interval set.
func Script(fwc config.Firewall) string {
	lan := map[string][]netip.Prefix{} // "tcp . 14238" -> hosts
	var icmp []netip.Prefix
	for _, r := range fwc.Allow {
		p, ok := prefix(r.Host)
		if !ok {
			continue
		}
		for _, port := range r.Ports {
			if port == "icmp" {
				icmp = append(icmp, p)
				continue
			}
			if k, ok := protoPort(port); ok {
				lan[k] = append(lan[k], p)
			}
		}
	}
	pi := map[string]bool{}
	for _, port := range fwc.PalmToPi {
		if k, ok := protoPort(port); ok {
			pi[k] = true
		}
	}

	var b strings.Builder
	for _, set := range []string{"lan_allow", "lan_allow_icmp", "pi_allow"} {
		fmt.Fprintf(&b, "flush set %s %s\n", Table, set)
	}
	var elems []string
	for _, k := range sortedKeys(lan) {
		for _, p := range minimal(lan[k]) {
			elems = append(elems, p.String()+" . "+k)
		}
	}
	addElements(&b, "lan_allow", elems)
	elems = elems[:0]
	for _, p := range minimal(icmp) {
		elems = append(elems, p.String())
	}
	addElements(&b, "lan_allow_icmp", elems)
	elems = elems[:0]
	for k := range pi {
		elems = append(elems, k)
	}
	sort.Strings(elems)
	addElements(&b, "pi_allow", elems)
	return b.String()
}

func addElements(b *strings.Builder, set string, elems []string) {
	if len(elems) == 0 {
		return
	}
	fmt.Fprintf(b, "add element %s %s { %s }\n", Table, set, strings.Join(elems, ", "))
}

// prefix parses an address or CIDR as a masked prefix; a bare address is a /32.
func prefix(host string) (netip.Prefix, bool) {
	if a, err := netip.ParseAddr(host); err == nil && a.Is4() {
		return netip.PrefixFrom(a, 32), true
	}
	if p, err := netip.ParsePrefix(host); err == nil && p.Addr().Is4() {
		return p.Masked(), true
	}
	return netip.Prefix{}, false
}

// protoPort turns "tcp/14238" into the nft concatenation "tcp . 14238".
func protoPort(s string) (string, bool) {
	proto, port, ok := strings.Cut(s, "/")
	if !ok || (proto != "tcp" && proto != "udp") {
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return proto + " . " + strconv.Itoa(n), true
}

// minimal drops duplicates and prefixes contained in another one, and sorts
// the rest. Two IPv4 prefixes either nest or are disjoint, so this leaves no
// overlap.
func minimal(ps []netip.Prefix) []netip.Prefix {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Bits() != ps[j].Bits() {
			return ps[i].Bits() < ps[j].Bits() // wider first
		}
		return ps[i].Addr().Less(ps[j].Addr())
	})
	var out []netip.Prefix
	for _, p := range ps {
		covered := false
		for _, q := range out {
			if q.Bits() <= p.Bits() && q.Contains(p.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Addr() != out[j].Addr() {
			return out[i].Addr().Less(out[j].Addr())
		}
		return out[i].Bits() < out[j].Bits()
	})
	return out
}

func sortedKeys(m map[string][]netip.Prefix) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Nft runs the nft binary.
type Nft struct {
	Path string // "" = nft from PATH
}

func (n Nft) path() string {
	if n.Path != "" {
		return n.Path
	}
	return "nft"
}

// Apply loads script in one transaction: either every set is replaced or
// none is.
func (n Nft) Apply(ctx context.Context, script string) error {
	return n.run(ctx, script, "-f", "-")
}

// Check parses script without applying it (nft -c).
func (n Nft) Check(ctx context.Context, script string) error {
	return n.run(ctx, script, "-c", "-f", "-")
}

func (n Nft) run(ctx context.Context, script string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, n.path(), args...)
	cmd.Stdin = strings.NewReader(script)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			return fmt.Errorf("nft %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return nil
}

// TablePresent reports whether the palm_gw table is loaded.
func (n Nft) TablePresent(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, n.path(), "list", "table", "inet", "palm_gw").Run() == nil
}

// Counter is one counted rule in the palm_gw table, named by its comment.
type Counter struct {
	Chain   string `json:"chain"`
	Comment string `json:"comment"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// Counters lists the palm_gw table's rules that have a counter and a comment:
// the drops (palm->rfc1918, palm->pi, ...) the web UI's firewall page shows
// so an operator can see what a Palm tried to reach (PLAN.md section 8.12).
func (n Nft) Counters(ctx context.Context) ([]Counter, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, n.path(), "-j", "list", "table", "inet", "palm_gw").Output()
	if err != nil {
		return nil, fmt.Errorf("nft -j list table inet palm_gw: %w", err)
	}
	return ParseCounters(out)
}

// ParseCounters reads `nft -j list table` output.
func ParseCounters(b []byte) ([]Counter, error) {
	var doc struct {
		Nftables []struct {
			Rule *struct {
				Chain   string            `json:"chain"`
				Comment string            `json:"comment"`
				Expr    []json.RawMessage `json:"expr"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("nft JSON: %w", err)
	}
	var out []Counter
	for _, item := range doc.Nftables {
		r := item.Rule
		if r == nil || r.Comment == "" {
			continue
		}
		for _, e := range r.Expr {
			var c struct {
				Counter *struct {
					Packets uint64 `json:"packets"`
					Bytes   uint64 `json:"bytes"`
				} `json:"counter"`
			}
			if json.Unmarshal(e, &c) == nil && c.Counter != nil {
				out = append(out, Counter{Chain: r.Chain, Comment: r.Comment, Packets: c.Counter.Packets, Bytes: c.Counter.Bytes})
				break
			}
		}
	}
	return out, nil
}
