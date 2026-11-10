package main

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"palm-gw/internal/bluez"
)

const ifaceNetwork = "org.bluez.Network1"

type panOpts struct {
	adapter, addr, netns string
	internet             bool
	url                  string
	pool                 string // "first-last" the lease must fall in
	hold                 time.Duration
}

// udhcpcScript configures the lease udhcpc got and prints it on one line for
// fake-palm to read. busybox udhcpc passes the lease in the environment.
const udhcpcScript = `#!/bin/sh
case "$1" in
deconfig)
	ip -4 addr flush dev "$interface"
	ip link set "$interface" up
	;;
bound|renew)
	ip -4 addr flush dev "$interface"
	ip addr add "$ip/${mask:-24}" dev "$interface"
	if [ -n "$FAKE_PALM_DEFAULT_ROUTE" ] && [ -n "$router" ]; then
		ip route replace default via "${router%% *}" dev "$interface"
	fi
	echo "LEASE ip=$ip mask=$mask router=$router dns=$dns server=$serverid lease=$lease"
	;;
esac
`

// cmdPan connects to the gateway's NAP as a PANU (PLAN.md section 9): BlueZ's
// Network1.Connect("nap") on the client adapter, then DHCP on the bnep
// interface (busybox udhcpc), then the same ping, DNS and HTTP checks as
// connect. BlueZ ends the connection when this process leaves the bus, so it
// stays running for --hold.
func cmdPan(o panOpts) int {
	first, last, err := parsePool(o.pool)
	if err != nil {
		logf("%v", err)
		return 2
	}
	if _, err := exec.LookPath("busybox"); err != nil {
		logf("busybox (for udhcpc) is not installed")
		return 1
	}
	bus, a, err := client(o.adapter)
	if err != nil {
		logf("%v", err)
		return 1
	}
	defer func() { _ = bus.Close() }()
	dev, err := findDevice(bus, a, o.addr)
	if err != nil {
		logf("%v", err)
		return 1
	}
	objs, err := bus.ManagedObjects()
	if err != nil {
		logf("%v", err)
		return 1
	}
	if _, ok := objs[dev.Path][ifaceNetwork]; !ok {
		logf("%s has no %s here: BlueZ on %s has not seen the gateway's NAP record. If NAP was registered after "+
			"this adapter paired, its cached services are stale: pair again (fake-palm pair) inside a pairing window.",
			o.addr, ifaceNetwork, a.HCI)
		return 1
	}
	if o.netns != "" {
		if err := ensureNetns(o.netns); err != nil {
			logf("%v", err)
			return 1
		}
	}

	net := bus.Object(dev.Path)
	if connected(bus, dev.Path) {
		// Left over from an earlier run whose far end went away; Network1.Disconnect
		// only works for the process that connected, so drop the link instead.
		logf("a PAN connection to %s is already up here; disconnecting it first", o.addr)
		_ = net.Call(bluez.IfaceDevice+".Disconnect", 0).Err
		for i := 0; i < 20 && connected(bus, dev.Path); i++ {
			time.Sleep(250 * time.Millisecond)
		}
	}
	t0 := time.Now()
	var ifname string
	if err := net.Call(ifaceNetwork+".Connect", 0, "nap").Store(&ifname); err != nil {
		logf("Network1.Connect(nap): %v", err)
		return 1
	}
	logf("PAN connected %s -> %s: %s in %s", a.Props.Address, o.addr, ifname, time.Since(t0).Round(time.Millisecond))
	defer func() {
		if err := net.Call(ifaceNetwork+".Disconnect", 0).Err; err != nil && connected(bus, dev.Path) {
			logf("Network1.Disconnect: %v", err)
		}
		logf("disconnected")
	}()

	if o.netns != "" {
		if out, err := exec.Command("ip", "link", "set", ifname, "netns", o.netns).CombinedOutput(); err != nil {
			logf("ip link set %s netns %s: %v: %s", ifname, o.netns, err, out)
			return 1
		}
		if out, err := exec.Command("ip", "-n", o.netns, "link", "set", ifname, "up").CombinedOutput(); err != nil {
			logf("ip -n %s link set %s up: %v: %s", o.netns, ifname, err, out)
			return 1
		}
		logf("%s moved into namespace %s", ifname, o.netns)
	}

	lease, err := dhcp(ifname, o.netns)
	if err != nil {
		logf("DHCP: %v", err)
		return 1
	}
	logf("DHCP lease after %s: %s", time.Since(t0).Round(time.Millisecond), lease["line"])
	rc := 0
	ip, err := netip.ParseAddr(lease["ip"])
	if err != nil || ip.Less(first) || last.Less(ip) {
		logf("FAIL: leased address %q is not in %s", lease["ip"], o.pool)
		rc = 1
	} else {
		logf("leased address %s is in %s", ip, o.pool)
	}
	gw := strings.Fields(lease["router"])
	if len(gw) == 0 {
		logf("FAIL: the lease has no router")
		return 1
	}
	if code := runProbe(o.netns, gw[0], o.internet, o.url); code != 0 {
		rc = code
	}
	if o.hold > 0 {
		// Disconnect on SIGTERM too: when this process just dies, BlueZ on the
		// client side tries to bring down a bnep interface that is in another
		// namespace, fails, and leaves the link up.
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigs)
		logf("holding the connection for %s (Ctrl-C to stop)", o.hold)
		deadline := time.Now().Add(o.hold)
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for time.Now().Before(deadline) {
			select {
			case s := <-sigs:
				logf("%s: disconnecting", s)
				return rc
			case <-tick.C:
			}
			if !connected(bus, dev.Path) {
				logf("PAN connection dropped: the gateway ended it")
				return rc
			}
		}
	}
	return rc
}

// connected reads Network1.Connected.
func connected(bus *bluez.Bus, p dbus.ObjectPath) bool {
	v, err := bus.Object(p).GetProperty(ifaceNetwork + ".Connected")
	if err != nil {
		return false
	}
	b, _ := v.Value().(bool)
	return b
}

// dhcp runs one busybox udhcpc exchange on ifname (inside netns if set) and
// returns the lease's fields plus "line", the script's whole output line.
func dhcp(ifname, netns string) (map[string]string, error) {
	dir, err := os.MkdirTemp("", "fake-palm-dhcp-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	script := filepath.Join(dir, "udhcpc.sh")
	if err := os.WriteFile(script, []byte(udhcpcScript), 0o755); err != nil {
		return nil, err
	}
	args := []string{"busybox", "udhcpc", "-i", ifname, "-f", "-q", "-n", "-t", "5", "-T", "2", "-s", script}
	if netns != "" {
		args = append([]string{"ip", "netns", "exec", netns}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	if netns != "" {
		// Only inside the namespace may the lease set a default route; on a
		// separate test machine the link stays a side route.
		cmd.Env = append(os.Environ(), "FAKE_PALM_DEFAULT_ROUTE=1")
	}
	out, err := cmd.CombinedOutput()
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "LEASE ") {
			continue
		}
		m := map[string]string{"line": strings.TrimPrefix(line, "LEASE ")}
		for _, kv := range strings.Fields(m["line"]) {
			if k, v, ok := strings.Cut(kv, "="); ok {
				m[k] = v
			}
		}
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("udhcpc: %w: %s", err, lastLines(string(out), 4))
	}
	return nil, fmt.Errorf("udhcpc got no lease: %s", lastLines(string(out), 4))
}

func parsePool(s string) (netip.Addr, netip.Addr, error) {
	a, b, ok := strings.Cut(s, "-")
	first, err1 := netip.ParseAddr(strings.TrimSpace(a))
	last, err2 := netip.ParseAddr(strings.TrimSpace(b))
	if !ok || err1 != nil || err2 != nil || last.Less(first) {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("--pool %q: want FIRST-LAST", s)
	}
	return first, last, nil
}
