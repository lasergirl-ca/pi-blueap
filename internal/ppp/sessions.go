package ppp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"palm-gw/internal/event"
)

// Config is what the manager needs from [network] and [ppp].
type Config struct {
	PPPD         string // path to pppd; "" = /usr/sbin/pppd
	OptionsFile  string
	Gateway      string
	PoolFirst    string
	PoolLast     string
	Debug        bool
	IdleSeconds  int
	MaxSessions  int
	PerDeviceMax int
	RunDir       string // where pppd writes ppp-<linkname>.pid; "" = /run
}

// Errors from Begin.
var (
	ErrFull      = errors.New("ppp: max_sessions reached")
	ErrCancelled = errors.New("ppp: session cancelled")
)

// Session states.
const (
	StateHandshake = "handshake" // fd taken, handshake running
	StatePPP       = "ppp"       // pppd running, IPCP not finished
	StateUp        = "up"        // pppd reported both IP addresses
)

// Info is one row of the session table, as `palm-gwctl status` shows it.
type Info struct {
	ID        uint64    `json:"id"`
	Addr      string    `json:"addr"`
	Name      string    `json:"name,omitempty"`
	Profile   string    `json:"profile"`
	State     string    `json:"state"`
	Handshake string    `json:"handshake,omitempty"`
	Ifname    string    `json:"ifname,omitempty"`
	Local     string    `json:"local,omitempty"`
	Peer      string    `json:"peer,omitempty"`
	PID       int       `json:"pid,omitempty"`
	Started   time.Time `json:"started"`
	RxBytes   uint64    `json:"rx_bytes,omitempty"` // from the Palm, /sys/class/net/<ifname>/statistics (filled by status)
	TxBytes   uint64    `json:"tx_bytes,omitempty"` // to the Palm
}

// Session is a handle on one connection from NewConnection to pppd exit.
type Session struct {
	m      *Manager
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	info   Info
	f      *os.File // the RFCOMM socket; kept until the session ends so HangUp can drop the link
	cmd    *exec.Cmd
	peer   netip.Addr
	ending bool   // replaced or disconnected; not counted against limits
	reason string // why it ended, for session_end
}

// Manager owns the session table and the address pool.
type Manager struct {
	cfg    Config
	log    *slog.Logger
	events *event.Bus
	pool   *Pool

	mu       sync.Mutex
	sessions map[uint64]*Session
	seq      uint64
}

// NewManager builds a manager; nothing runs until Begin.
func NewManager(cfg Config, log *slog.Logger, events *event.Bus) (*Manager, error) {
	if cfg.PPPD == "" {
		cfg.PPPD = "/usr/sbin/pppd"
	}
	if cfg.RunDir == "" {
		cfg.RunDir = "/run"
	}
	if cfg.MaxSessions < 1 {
		cfg.MaxSessions = 1
	}
	if cfg.PerDeviceMax < 1 {
		cfg.PerDeviceMax = 1
	}
	pool, err := NewPool(cfg.PoolFirst, cfg.PoolLast)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = event.New()
	}
	return &Manager{cfg: cfg, log: log.With("component", "ppp"), events: events, pool: pool, sessions: map[uint64]*Session{}}, nil
}

// Begin registers a new connection that has just been handed over by BlueZ.
// It enforces per_device_max by marking the oldest live sessions of the same
// device for replacement (returned, for the caller to Terminate off the D-Bus
// thread) and max_sessions by refusing with ErrFull. The caller owns f until
// Start or Abort.
func (m *Manager) Begin(addr, name, profile string, f *os.File) (*Session, []*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var same []*Session
	active := 0
	for _, s := range m.sessions {
		if s.isEnding() {
			continue
		}
		active++
		if s.info.Addr == addr {
			same = append(same, s)
		}
	}
	sort.Slice(same, func(i, j int) bool { return same[i].info.ID < same[j].info.ID })
	var replaced []*Session
	for len(same) >= m.cfg.PerDeviceMax {
		old := same[0]
		same = same[1:]
		old.markEnding("replaced")
		replaced = append(replaced, old)
		active--
	}
	if active >= m.cfg.MaxSessions {
		for _, old := range replaced { // nothing was replaced after all
			old.unmarkEnding()
		}
		return nil, nil, ErrFull
	}
	m.seq++
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		m: m, ctx: ctx, cancel: cancel, done: make(chan struct{}), f: f,
		info: Info{ID: m.seq, Addr: addr, Name: name, Profile: profile, State: StateHandshake, Started: time.Now()},
	}
	m.sessions[s.info.ID] = s
	return s, replaced, nil
}

// Context is cancelled when the session is replaced or disconnected during
// the handshake.
func (s *Session) Context() context.Context { return s.ctx }

// Info returns a copy of the session's row.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Done is closed when the session has fully ended.
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) isEnding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ending
}

func (s *Session) markEnding(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ending {
		s.ending = true
		s.reason = reason
	}
}

func (s *Session) unmarkEnding() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ending = false
	s.reason = ""
}

// Abort ends a session that never reached pppd: closes the fd and removes it.
func (s *Session) Abort(reason string) {
	s.mu.Lock()
	if s.cmd != nil {
		s.mu.Unlock()
		return
	}
	if s.reason == "" {
		s.reason = reason
	}
	s.mu.Unlock()
	s.finish(-1)
}

// HangUp drops the RFCOMM link and closes f. bluetoothd keeps its own reference
// to the socket after NewConnection (DECISIONS.md D-028), so closing ours, or
// pppd exiting, would leave the link up and the Palm still connected. shutdown
// acts on the socket itself, so the channel closes whoever else holds it. On
// something that is not a socket (tests) it is just a close.
func HangUp(f *os.File) {
	if rc, err := f.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) { _ = unix.Shutdown(int(fd), unix.SHUT_RDWR) })
	}
	_ = f.Close()
}

// Start allocates a peer address and runs pppd on the fd (PLAN.md section
// 8.5). The daemon keeps its copy of the fd so finish can hang up (D-028).
func (s *Session) Start(handshake string) error {
	m := s.m
	peer, err := m.pool.Get()
	if err != nil {
		m.log.Warn("session refused", "event", "session_refused", "reason", "pool_exhausted", "addr", s.info.Addr)
		m.events.Emit("session_refused", s.info.Addr, s.info.Name, "reason", "pool_exhausted")
		s.Abort("pool_exhausted")
		return err
	}
	b := peer.As4()
	args := []string{
		"notty", "file", m.cfg.OptionsFile, "noauth", "nocrtscts",
		m.cfg.Gateway + ":" + peer.String(),
		"ms-dns", m.cfg.Gateway,
		"linkname", fmt.Sprintf("palm-%d", b[3]),
		"ipparam", s.info.Addr,
	}
	if m.cfg.Debug {
		args = append(args, "debug")
	}
	if m.cfg.IdleSeconds > 0 {
		args = append(args, "idle", strconv.Itoa(m.cfg.IdleSeconds))
	}
	cmd := exec.Command(m.cfg.PPPD, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}

	s.mu.Lock()
	if s.ctx.Err() != nil || s.ending {
		s.mu.Unlock()
		m.pool.Put(peer)
		s.Abort("cancelled")
		return ErrCancelled
	}
	cmd.Stdin, cmd.Stdout = s.f, s.f
	// A plain pipe, not cmd.StderrPipe: Wait must not depend on the log
	// reaching EOF, because anything left in pppd's process group would keep
	// the write end open after pppd itself has exited.
	stderr, stderrW, err := os.Pipe()
	if err == nil {
		cmd.Stderr = stderrW
		err = cmd.Start()
		_ = stderrW.Close()
		if err != nil {
			_ = stderr.Close()
		}
	}
	if err != nil {
		s.mu.Unlock()
		m.pool.Put(peer)
		m.log.Error("pppd failed to start", "addr", s.info.Addr, "err", err)
		s.Abort("spawn_failed")
		return err
	}
	s.cmd = cmd
	s.peer = peer
	s.info.PID = cmd.Process.Pid
	s.info.State = StatePPP
	s.info.Handshake = handshake
	s.info.Local = m.cfg.Gateway
	s.info.Peer = peer.String()
	info := s.info
	s.mu.Unlock()

	m.log.Info("session started", "event", "session_start", "addr", info.Addr, "name", info.Name,
		"profile", info.Profile, "handshake", handshake, "peer", info.Peer, "pid", info.PID)
	m.events.Emit("session_start", info.Addr, info.Name, "profile", info.Profile, "peer", info.Peer, "handshake", handshake)

	logDone := make(chan struct{})
	go s.readLog(stderr, logDone)
	go s.pidFileFallback(fmt.Sprintf("ppp-palm-%d.pid", b[3]))
	go func() {
		_ = cmd.Wait()
		code := cmd.ProcessState.ExitCode()
		// Make sure nothing in the group (the notty shunt) outlives pppd;
		// that also closes the last writer of the log pipe.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-logDone:
		case <-time.After(2 * time.Second):
			m.log.Warn("pppd log still open after exit", "addr", info.Addr, "pid", info.PID)
		}
		_ = stderr.Close()
		m.pool.Put(peer)
		s.finish(code)
	}()
	return nil
}

var (
	reIface  = regexp.MustCompile(`^Using interface (ppp\d+)`)
	reLocal  = regexp.MustCompile(`^local\s+IP address (\S+)`)
	reRemote = regexp.MustCompile(`^remote IP address (\S+)`)
)

// readLog logs pppd's stderr line by line and picks out the interface name
// and the negotiated addresses.
func (s *Session) readLog(r io.Reader, done chan<- struct{}) {
	defer close(done)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		s.mu.Lock()
		if mm := reIface.FindStringSubmatch(line); mm != nil {
			s.info.Ifname = mm[1]
		}
		if mm := reLocal.FindStringSubmatch(line); mm != nil {
			s.info.Local = mm[1]
		}
		up := false
		if mm := reRemote.FindStringSubmatch(line); mm != nil {
			s.info.Peer = mm[1]
			if s.info.State != StateUp {
				s.info.State = StateUp
				up = true
			}
		}
		info := s.info
		s.mu.Unlock()
		s.m.log.Info(line, "addr", info.Addr, "ifname", info.Ifname, "pid", info.PID)
		if up {
			s.m.log.Info("PPP link up", "event", "session_up", "addr", info.Addr, "name", info.Name,
				"ifname", info.Ifname, "local", info.Local, "peer", info.Peer)
			s.m.events.Emit("session_up", info.Addr, info.Name, "ifname", info.Ifname, "peer", info.Peer)
		}
	}
	_, _ = io.Copy(io.Discard, r)
}

// pidFileFallback reads /run/ppp-palm-<n>.pid (pid, then interface name)
// if pppd's log has not named the interface within 5 s.
func (s *Session) pidFileFallback(name string) {
	select {
	case <-s.done:
		return
	case <-time.After(5 * time.Second):
	}
	s.mu.Lock()
	known := s.info.Ifname != ""
	s.mu.Unlock()
	if known {
		return
	}
	b, err := os.ReadFile(filepath.Join(s.m.cfg.RunDir, name))
	if err != nil {
		return
	}
	lines := strings.Fields(string(b))
	if len(lines) >= 2 && strings.HasPrefix(lines[1], "ppp") {
		s.mu.Lock()
		s.info.Ifname = lines[1]
		s.mu.Unlock()
	}
}

// finish removes the session and logs session_end. exit is pppd's exit code,
// or -1 if pppd never ran.
func (s *Session) finish(exit int) {
	m := s.m
	m.mu.Lock()
	_, ok := m.sessions[s.info.ID]
	delete(m.sessions, s.info.ID)
	m.mu.Unlock()
	if !ok {
		return
	}
	s.cancel()
	s.mu.Lock()
	info, reason, f := s.info, s.reason, s.f
	s.f = nil
	s.mu.Unlock()
	if f != nil {
		HangUp(f)
	}
	if reason == "" {
		reason = "pppd_exit"
	}
	dur := time.Since(info.Started).Round(time.Second)
	m.log.Info("session ended", "event", "session_end", "addr", info.Addr, "name", info.Name,
		"profile", info.Profile, "ifname", info.Ifname, "peer", info.Peer, "duration", dur.String(),
		"exit", exit, "reason", reason)
	m.events.Emit("session_end", info.Addr, info.Name, "ifname", info.Ifname, "duration", dur.String(),
		"exit", strconv.Itoa(exit), "reason", reason)
	close(s.done)
}

// Terminate ends s: cancels its handshake, or SIGTERMs pppd's process group,
// then SIGKILLs it after 2 s. It returns once the session has ended or after
// about 4 s.
func (s *Session) Terminate(reason string) {
	s.markEnding(reason)
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil {
		s.cancel()
		select {
		case <-s.done:
		case <-time.After(4 * time.Second):
			s.m.log.Warn("session did not end after cancel", "addr", s.info.Addr)
		}
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-s.done:
		return
	case <-time.After(2 * time.Second):
	}
	s.m.log.Warn("pppd ignored SIGTERM; killing", "addr", s.info.Addr, "pid", pgid)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		s.m.log.Error("pppd process group survived SIGKILL", "addr", s.info.Addr, "pid", pgid)
	}
}

// Disconnect terminates every session of addr; it returns how many there were.
func (m *Manager) Disconnect(addr, reason string) int {
	var victims []*Session
	m.mu.Lock()
	for _, s := range m.sessions {
		if s.info.Addr == addr {
			victims = append(victims, s)
		}
	}
	m.mu.Unlock()
	terminateAll(victims, reason)
	return len(victims)
}

// Shutdown terminates every session.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	terminateAll(all, "shutdown")
}

func terminateAll(ss []*Session, reason string) {
	var wg sync.WaitGroup
	for _, s := range ss {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			s.Terminate(reason)
		}(s)
	}
	wg.Wait()
}

// Sessions lists the table, oldest first.
func (m *Manager) Sessions() []Info {
	m.mu.Lock()
	ss := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		ss = append(ss, s)
	}
	m.mu.Unlock()
	out := make([]Info, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PoolInUse is the number of peer addresses handed out.
func (m *Manager) PoolInUse() int { return m.pool.InUse() }

func (m *Manager) knownPGIDs() map[int]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int]bool{}
	for _, s := range m.sessions {
		s.mu.Lock()
		if s.cmd != nil {
			out[s.cmd.Process.Pid] = true
		}
		s.mu.Unlock()
	}
	return out
}
