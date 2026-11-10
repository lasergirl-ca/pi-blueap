package ppp

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakePPPD writes a script that behaves enough like pppd for the manager:
// logs the interface and addresses on stderr, starts a child in its process
// group (like the notty shunt) and records the child's pid.
func fakePPPD(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "pppd")
	script := "#!/bin/sh\n" +
		"echo 'Using interface ppp7' >&2\n" +
		"echo 'local  IP address 10.42.0.1' >&2\n" +
		"echo 'remote IP address 10.42.0.10' >&2\n" +
		"sleep 300 & echo $! > " + filepath.Join(dir, "child.$$") + "\n" +
		body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func socketFile(t *testing.T) *os.File {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[1]) })
	return os.NewFile(uintptr(fds[0]), "rfcomm")
}

func newManager(t *testing.T, pppd string, maxSessions int) *Manager {
	t.Helper()
	m, err := NewManager(Config{
		PPPD: pppd, OptionsFile: "/dev/null", Gateway: "10.42.0.1",
		PoolFirst: "10.42.0.10", PoolLast: "10.42.0.12",
		MaxSessions: maxSessions, PerDeviceMax: 1, RunDir: t.TempDir(),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func childPID(t *testing.T, dir string, pppdPID int) int {
	t.Helper()
	var pid int
	waitFor(t, "child pid file", 3*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "child."+strconv.Itoa(pppdPID)))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	return pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func start(t *testing.T, m *Manager, addr string) *Session {
	t.Helper()
	s, replaced, err := m.Begin(addr, "Palm", "spp", socketFile(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range replaced {
		r.Terminate("replaced")
	}
	if err := s.Start("ms_null_modem"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionLifecycleAndDisconnect(t *testing.T) {
	dir := t.TempDir()
	m := newManager(t, fakePPPD(t, dir, "wait"), 8)
	s := start(t, m, "00:07:E0:00:00:01")
	waitFor(t, "state up", 3*time.Second, func() bool { return s.Info().State == StateUp })
	info := s.Info()
	if info.Ifname != "ppp7" || info.Peer != "10.42.0.10" || info.Local != "10.42.0.1" || info.Handshake != "ms_null_modem" {
		t.Errorf("info %+v", info)
	}
	if n := len(m.Sessions()); n != 1 || m.PoolInUse() != 1 {
		t.Fatalf("sessions %d pool %d", n, m.PoolInUse())
	}
	child := childPID(t, dir, info.PID)

	t0 := time.Now()
	if n := m.Disconnect("00:07:E0:00:00:01", "disconnect"); n != 1 {
		t.Fatalf("Disconnect = %d", n)
	}
	if el := time.Since(t0); el > 3*time.Second {
		t.Errorf("disconnect took %s", el)
	}
	waitFor(t, "group gone", 2*time.Second, func() bool { return !alive(child) && !alive(info.PID) })
	if len(m.Sessions()) != 0 || m.PoolInUse() != 0 {
		t.Errorf("after disconnect: sessions %d pool %d", len(m.Sessions()), m.PoolInUse())
	}
	if n := m.Disconnect("00:07:E0:00:00:01", "disconnect"); n != 0 {
		t.Errorf("second Disconnect = %d", n)
	}
}

func TestPerDeviceReplacementAndMaxSessions(t *testing.T) {
	dir := t.TempDir()
	m := newManager(t, fakePPPD(t, dir, "wait"), 2)
	a1 := start(t, m, "AA:00:00:00:00:01")
	a2 := start(t, m, "AA:00:00:00:00:01") // replaces a1
	select {
	case <-a1.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("old session not replaced")
	}
	if ss := m.Sessions(); len(ss) != 1 || ss[0].ID != a2.Info().ID {
		t.Fatalf("after replace: %+v", ss)
	}
	start(t, m, "BB:00:00:00:00:02")
	f := socketFile(t)
	if _, _, err := m.Begin("CC:00:00:00:00:03", "", "spp", f); !errors.Is(err, ErrFull) {
		t.Errorf("third device: err = %v, want ErrFull", err)
	}
	// A device that already has a session may still replace it when full.
	s, replaced, err := m.Begin("BB:00:00:00:00:02", "", "lap", socketFile(t))
	if err != nil || len(replaced) != 1 {
		t.Fatalf("replace when full: %v %d", err, len(replaced))
	}
	replaced[0].Terminate("replaced")
	s.Abort("test")
	m.Shutdown()
	if len(m.Sessions()) != 0 || m.PoolInUse() != 0 {
		t.Errorf("after shutdown: sessions %d pool %d", len(m.Sessions()), m.PoolInUse())
	}
}

func TestTerminateDuringHandshake(t *testing.T) {
	m := newManager(t, "/nonexistent", 8)
	s, _, err := m.Begin("AA:00:00:00:00:01", "", "dun", socketFile(t))
	if err != nil {
		t.Fatal(err)
	}
	go func() { // the profile goroutine: handshake until cancelled
		<-s.Context().Done()
		s.Abort("cancelled")
	}()
	t0 := time.Now()
	s.Terminate("disconnect")
	if time.Since(t0) > time.Second || len(m.Sessions()) != 0 {
		t.Errorf("terminate in handshake: %s, %d left", time.Since(t0), len(m.Sessions()))
	}
	// Start after being replaced must not spawn anything.
	s2, _, _ := m.Begin("AA:00:00:00:00:01", "", "dun", socketFile(t))
	_, repl, _ := m.Begin("AA:00:00:00:00:01", "", "dun", socketFile(t))
	if len(repl) != 1 || repl[0] != s2 {
		t.Fatal("expected s2 to be replaced")
	}
	if err := s2.Start("none"); !errors.Is(err, ErrCancelled) {
		t.Errorf("Start of a replaced session: %v", err)
	}
	if m.PoolInUse() != 0 {
		t.Error("pool leaked")
	}
}

func TestSIGTERMIgnoredAndSelfExit(t *testing.T) {
	dir := t.TempDir()
	m := newManager(t, fakePPPD(t, dir, "trap '' TERM\nwhile :; do sleep 1; done"), 8)
	s := start(t, m, "AA:00:00:00:00:01")
	child := childPID(t, dir, s.Info().PID)
	t0 := time.Now()
	m.Disconnect("AA:00:00:00:00:01", "disconnect")
	if el := time.Since(t0); el < 1500*time.Millisecond || el > 4*time.Second {
		t.Errorf("SIGKILL fallback took %s", el)
	}
	waitFor(t, "group killed", 2*time.Second, func() bool { return !alive(child) })

	m2 := newManager(t, fakePPPD(t, t.TempDir(), "exit 16"), 8)
	s2 := start(t, m2, "AA:00:00:00:00:02")
	select {
	case <-s2.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("self-exit not noticed")
	}
	if m2.PoolInUse() != 0 || len(m2.Sessions()) != 0 {
		t.Error("self-exit leaked")
	}
}

func TestOrphans(t *testing.T) {
	root := t.TempDir()
	old := ProcRoot
	ProcRoot = root
	t.Cleanup(func() { ProcRoot = old })
	mk := func(pid, comm, cmdline, stat string) {
		d := filepath.Join(root, pid)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmdline), 0o644)
		_ = os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644)
	}
	mk("100", "pppd", "/usr/sbin/pppd\x00notty\x00linkname\x00palm-10\x00", "100 (pppd) S 1 100 100 0")
	mk("101", "pppd", "pppd\x00notty\x00file\x00/dev/null\x00", "101 (pppd) S 1 101 101 0")
	mk("102", "bash", "bash\x00", "102 (bash) S 1 102 102 0")
	mk("103", "pppd", "/usr/sbin/pppd\x00linkname\x00palm-11\x00", "103 (pp d) S 1 103 103 0")
	mk("self", "x", "", "")
	m := newManager(t, "/nonexistent", 8)
	got := m.Orphans()
	if len(got) != 2 || got[0].PID != 100 || got[1].PGID != 103 {
		t.Errorf("orphans %+v", got)
	}
}

// TestHangUpDropsLinkDespiteOtherHolders mirrors bluetoothd keeping its own
// reference to the RFCOMM socket (D-028): closing our fd alone leaves the peer
// connected, HangUp ends it.
func TestHangUpDropsLinkDespiteOtherHolders(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	ours := os.NewFile(uintptr(fds[0]), "ours")
	peer := os.NewFile(uintptr(fds[1]), "peer")
	defer func() { _ = peer.Close() }()
	other, err := unix.Dup(fds[0]) // bluetoothd's copy
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(other) }()

	got := make(chan error, 1)
	go func() {
		_, err := peer.Read(make([]byte, 1))
		got <- err
	}()
	HangUp(ours)
	select {
	case err := <-got:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("peer read: %v, want EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer still connected after HangUp")
	}
}
