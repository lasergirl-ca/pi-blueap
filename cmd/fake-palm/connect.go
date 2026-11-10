package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"palm-gw/internal/ctl"
)

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s fake-palm: %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, a...))
}

// localAddress is the client adapter's Bluetooth address.
func localAddress(adapter string) (string, error) {
	bus, a, err := client(adapter)
	if err != nil {
		return "", err
	}
	defer func() { _ = bus.Close() }()
	return a.Props.Address, nil
}

// waitFor reads from fd until want appears or timeout passes.
func waitFor(fd int, want string, timeout time.Duration) (string, error) {
	var got []byte
	buf := make([]byte, 256)
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		ms := int(time.Until(end) / time.Millisecond)
		if ms < 1 {
			ms = 1
		}
		pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(pfd, ms)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return string(got), err
		}
		if n == 0 {
			break
		}
		m, err := unix.Read(fd, buf)
		if err != nil {
			return string(got), err
		}
		if m == 0 {
			return string(got), io.EOF
		}
		got = append(got, buf[:m]...)
		if strings.Contains(string(got), want) {
			return string(got), nil
		}
	}
	return string(got), fmt.Errorf("no %q within %s (got %q)", want, timeout, got)
}

// clientHandshake plays the Palm's side of section 8.4.
func clientHandshake(f *os.File, mode string) error {
	fd := int(f.Fd())
	send := func(s string) error { _, err := unix.Write(fd, []byte(s)); return err }
	switch mode {
	case "client":
		if err := send("CLIENT"); err != nil {
			return err
		}
		got, err := waitFor(fd, "CLIENTSERVER", 3*time.Second)
		if err != nil {
			return err
		}
		logf("sent CLIENT, got %q", got)
	case "at", "at-tx":
		steps := [][2]string{{"ATZ\r", "OK"}, {"ATDT0\r", "CONNECT"}}
		if mode == "at-tx" {
			// What a real Palm T|X sends (TESTING.md, Phase 2): a 3.5 s
			// pause first (D-023), then the dial last in a compound line
			// (D-024).
			time.Sleep(3500 * time.Millisecond)
			steps = [][2]string{
				{"ATQ0V1Z\r", "OK"},
				{"ATQ0E0\r", "OK"},
				{"ATQ0E0&D0&C1V1S7=90L1DT0\r", "CONNECT"},
			}
		}
		for _, step := range steps {
			if err := send(step[0]); err != nil {
				return err
			}
			got, err := waitFor(fd, step[1], 3*time.Second)
			if err != nil {
				return err
			}
			logf("sent %q, got %q", step[0], got)
		}
	case "ppp":
	default:
		return fmt.Errorf("unknown mode %q (client, ppp, at or at-tx)", mode)
	}
	return nil
}

var (
	reIface  = regexp.MustCompile(`^Using interface (ppp\d+)`)
	reLocal  = regexp.MustCompile(`^local\s+IP address (\S+)`)
	reRemote = regexp.MustCompile(`^remote IP address (\S+)`)
)

type pppLink struct {
	cmd    *exec.Cmd
	ifname string
	local  string
	remote string
	exited chan struct{}
	up     chan struct{}
}

// ensureNetns creates the network namespace the client pppd runs in, so its
// interface and routes do not collide with the gateway's on the same host.
func ensureNetns(name string) error {
	if _, err := os.Stat("/run/netns/" + name); err == nil {
		return nil
	}
	if out, err := exec.Command("ip", "netns", "add", name).CombinedOutput(); err != nil {
		return fmt.Errorf("ip netns add %s: %w: %s", name, err, out)
	}
	if out, err := exec.Command("ip", "-n", name, "link", "set", "lo", "up").CombinedOutput(); err != nil {
		return fmt.Errorf("ip -n %s link set lo up: %w: %s", name, err, out)
	}
	return nil
}

// startPPPD runs the client pppd from PLAN.md section 9 on f.
func startPPPD(f *os.File, netns string, verbose bool) (*pppLink, error) {
	args := []string{"notty", "local", "noauth", "nocrtscts", "nodetach", "logfd", "2", "noipdefault",
		"ipcp-accept-local", "ipcp-accept-remote", "novj", "noccp", "file", "/dev/null"}
	if netns != "" {
		// Inside its own namespace the link may as well be the default route. No usepeerdns:
		// a namespace shares /etc, so Debian's ip-up.d/0000usepeerdns hook would rewrite the
		// host's resolv.conf to the gateway address, and on the gateway itself that leaves
		// dnsmasq with no upstream (D-027). The probe asks the gateway directly anyway.
		args = append(args, "defaultroute")
	} else {
		args = append(args, "usepeerdns", "nodefaultroute")
	}
	name := "/usr/sbin/pppd"
	if netns != "" {
		args = append([]string{"netns", "exec", netns, name}, args...)
		name = "ip"
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		_ = r.Close()
		return nil, err
	}
	_ = w.Close()
	l := &pppLink{cmd: cmd, exited: make(chan struct{}), up: make(chan struct{})}
	upCh := make(chan struct{}, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if verbose {
				logf("pppd: %s", line)
			}
			if m := reIface.FindStringSubmatch(line); m != nil {
				l.ifname = m[1]
			}
			if m := reLocal.FindStringSubmatch(line); m != nil {
				l.local = m[1]
			}
			if m := reRemote.FindStringSubmatch(line); m != nil {
				l.remote = m[1]
				select {
				case upCh <- struct{}{}:
				default:
				}
			}
		}
	}()
	go func() {
		<-upCh
		close(l.up)
	}()
	go func() {
		_ = cmd.Wait()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = r.Close()
		close(l.exited)
	}()
	return l, nil
}

func (l *pppLink) stop() {
	_ = syscall.Kill(-l.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-l.exited:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-l.cmd.Process.Pid, syscall.SIGKILL)
		<-l.exited
	}
}

type connectOpts struct {
	adapter, addr, mode, netns, url string
	channel                         int
	internet, verbose               bool
	hold                            time.Duration
	upTimeout                       time.Duration
}

func cmdConnect(o connectOpts) int {
	local, err := localAddress(o.adapter)
	if err != nil {
		logf("%v", err)
		return 1
	}
	if o.netns != "" {
		if err := ensureNetns(o.netns); err != nil {
			logf("%v", err)
			return 1
		}
	}
	t0 := time.Now()
	f, err := dialRFCOMM(local, o.addr, o.channel)
	if err != nil {
		logf("%v", err)
		return 1
	}
	defer func() { _ = f.Close() }()
	logf("RFCOMM connected %s -> %s channel %d in %s", local, o.addr, o.channel, time.Since(t0).Round(time.Millisecond))
	if err := clientHandshake(f, o.mode); err != nil {
		logf("handshake (%s) failed: %v", o.mode, err)
		return 1
	}
	link, err := startPPPD(f, o.netns, o.verbose)
	if err != nil {
		logf("pppd: %v", err)
		return 1
	}
	_ = f.Close() // pppd has it now
	defer link.stop()
	select {
	case <-link.up:
	case <-link.exited:
		logf("pppd exited before IPCP finished (run with --verbose to see its log)")
		return 1
	case <-time.After(o.upTimeout):
		logf("PPP not up within %s", o.upTimeout)
		return 1
	}
	logf("PPP up: %s local %s remote %s (%s after connect)", link.ifname, link.local, link.remote, time.Since(t0).Round(time.Millisecond))
	rc := 0
	if !strings.HasPrefix(link.local, "10.42.0.") {
		logf("FAIL: local address %s is not in 10.42.0.0/24", link.local)
		rc = 1
	}
	if code := runProbe(o.netns, link.remote, o.internet, o.url); code != 0 {
		rc = code
	}
	if o.hold > 0 {
		logf("holding the link for %s (Ctrl-C to stop)", o.hold)
		select {
		case <-link.exited:
			logf("pppd exited: the gateway ended the link")
			return rc
		case <-time.After(o.hold):
		}
	}
	link.stop()
	logf("disconnected")
	return rc
}

// runProbe tests the link, inside the namespace when there is one.
func runProbe(netns, gateway string, internet bool, url string) int {
	if netns == "" {
		return probe(gateway, internet, url)
	}
	self, err := os.Executable()
	if err != nil {
		logf("%v", err)
		return 1
	}
	args := []string{"netns", "exec", netns, self, "probe", "--gateway", gateway}
	if internet {
		args = append(args, "--internet", "--url", url)
	}
	cmd := exec.Command("ip", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		logf("probe: %v", err)
		return 1
	}
	return 0
}

// defaultURL is the page section 9 names. --url swaps it when that host is down (D-027).
const defaultURL = "http://neverssl.com/"

// probe pings the gateway, resolves a name through it, and optionally
// fetches page (PLAN.md section 9, "run pppd").
func probe(gateway string, internet bool, page string) int {
	rc := 0
	out, err := exec.Command("ping", "-c", "3", "-W", "2", gateway).CombinedOutput()
	summary := lastLines(string(out), 2)
	if err != nil {
		logf("FAIL: ping %s: %v\n%s", gateway, err, summary)
		rc = 1
	} else {
		logf("ping %s ok: %s", gateway, strings.ReplaceAll(summary, "\n", "; "))
	}
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(gateway, "53"))
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	addrs, err := res.LookupHost(ctx, "neverssl.com")
	cancel()
	if err != nil {
		logf("FAIL: DNS through %s: %v", gateway, err)
		return 1
	}
	logf("DNS through %s ok: neverssl.com = %s", gateway, strings.Join(addrs, ", "))
	if !internet {
		return rc
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := res.LookupHost(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("resolve %s: no addresses", host)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
	}}
	hc := &http.Client{Transport: tr, Timeout: 15 * time.Second}
	t0 := time.Now()
	resp, err := hc.Get(page)
	if err != nil {
		logf("FAIL: GET %s: %v", page, err)
		return 1
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logf("FAIL: GET %s: %s", page, resp.Status)
		return 1
	}
	logf("GET %s ok: %s, %d bytes in %s", page, resp.Status, n, time.Since(t0).Round(time.Millisecond))
	return rc
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

type stormOpts struct {
	adapter, addr, netns, ctlSocket string
	count                           int
	interval                        time.Duration
	channels                        []int
}

// cmdStorm connects and disconnects rapidly in three patterns: close during
// the handshake, run PPP briefly then hang up, and open a second connection
// on another channel while the first is still up (per_device_max). Then it
// checks the gateway has no sessions and no pppd left.
func cmdStorm(o stormOpts) int {
	local, err := localAddress(o.adapter)
	if err != nil {
		logf("%v", err)
		return 1
	}
	if o.netns != "" {
		if err := ensureNetns(o.netns); err != nil {
			logf("%v", err)
			return 1
		}
	}
	modeFor := map[int]string{1: "client", 2: "ppp", 3: "at"}
	failures := 0
	for i := 0; i < o.count; i++ {
		ch := o.channels[i%len(o.channels)]
		f, err := dialRFCOMM(local, o.addr, ch)
		if err != nil {
			logf("[%d] connect ch%d: %v", i, ch, err)
			failures++
			time.Sleep(o.interval)
			continue
		}
		switch i % 3 {
		case 0: // hang up during the handshake
			logf("[%d] ch%d: connect, hang up at once", i, ch)
			_ = f.Close()
		case 1: // PPP for a moment
			if err := clientHandshake(f, modeFor[ch]); err != nil {
				logf("[%d] ch%d handshake: %v", i, ch, err)
				_ = f.Close()
				failures++
				break
			}
			link, err := startPPPD(f, o.netns, false)
			_ = f.Close()
			if err != nil {
				logf("[%d] pppd: %v", i, err)
				failures++
				break
			}
			d := time.Duration(300+rand.IntN(1200)) * time.Millisecond
			select {
			case <-link.up:
				logf("[%d] ch%d: PPP up as %s, hanging up after %s", i, ch, link.local, d)
			case <-time.After(5 * time.Second):
				logf("[%d] ch%d: PPP not up in 5 s, hanging up", i, ch)
			}
			time.Sleep(d)
			link.stop()
		case 2: // overlap: a second channel while the first is open
			other := o.channels[(i+1)%len(o.channels)]
			if other == ch {
				other = ch%3 + 1
			}
			_ = clientHandshake(f, modeFor[ch])
			g, err := dialRFCOMM(local, o.addr, other)
			if err != nil {
				logf("[%d] overlap ch%d+ch%d: second connect: %v", i, ch, other, err)
			} else {
				logf("[%d] overlap: ch%d then ch%d while ch%d is open", i, ch, other, ch)
				_ = clientHandshake(g, modeFor[other])
				time.Sleep(500 * time.Millisecond)
				_ = g.Close()
			}
			_ = f.Close()
		}
		time.Sleep(o.interval)
	}
	logf("storm done: %d iterations, %d client-side failures; waiting 6 s for the gateway to settle", o.count, failures)
	time.Sleep(6 * time.Second)
	if o.ctlSocket == "" {
		logf("no --ctl socket given; check `palm-gwctl status` on the gateway by hand")
		return 0
	}
	var st ctl.StatusReply
	if err := ctl.Call(o.ctlSocket, ctl.Request{Cmd: "status"}, &st); err != nil {
		logf("status from %s: %v", o.ctlSocket, err)
		return 1
	}
	pppds := countGatewayPPPD()
	logf("gateway: %d sessions, %d pool addresses in use, %d gateway pppd processes", len(st.Sessions), st.PoolInUse, pppds)
	if len(st.Sessions) != 0 || st.PoolInUse != 0 || pppds > 0 {
		logf("FAIL: the gateway did not clean up")
		return 1
	}
	logf("PASS: zero sessions and zero pppd left")
	return 0
}

// countGatewayPPPD counts pppd processes with the gateway's `linkname palm-`
// on this host (-1 if /proc is unreadable).
func countGatewayPPPD() int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err == nil && strings.Contains(string(b), "linkname\x00palm-") {
			n++
		}
	}
	return n
}
