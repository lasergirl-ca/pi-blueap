package profile

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"palm-gw/internal/config"
)

func hsConfig() config.Handshake {
	return config.Handshake{MSNullModem: true, MSTimeoutMS: 400, ATEmulator: true, ATConnectBanner: "CONNECT 115200", ATTimeoutMS: 1000}
}

type step struct {
	send  string
	delay time.Duration
}

type pair struct {
	srv, cli int
}

func newPair(t *testing.T) pair {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]); _ = unix.Close(fds[1]) })
	return pair{srv: fds[0], cli: fds[1]}
}

// drain reads whatever is waiting on fd within d.
func drain(t *testing.T, fd int, d time.Duration) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, 256)
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		ok, err := waitReadable(fd, 20*time.Millisecond)
		if err != nil || !ok {
			continue
		}
		n, err := unix.Read(fd, buf)
		if err != nil || n == 0 {
			break
		}
		out = append(out, buf[:n]...)
	}
	return out
}

func runCase(t *testing.T, k Kind, cfg config.Handshake, steps []step) (Result, []byte, []byte, error) {
	t.Helper()
	p := newPair(t)
	type ret struct {
		r   Result
		err error
	}
	done := make(chan ret, 1)
	go func() {
		r, err := Run(context.Background(), p.srv, k, cfg)
		done <- ret{r, err}
	}()
	for _, s := range steps {
		time.Sleep(s.delay)
		if s.send != "" {
			if _, err := unix.Write(p.cli, []byte(s.send)); err != nil {
				t.Fatal(err)
			}
		}
	}
	var r ret
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handshake did not return")
	}
	replies := drain(t, p.cli, 50*time.Millisecond)
	left := drain(t, p.srv, 50*time.Millisecond)
	return r.r, replies, left, r.err
}

func TestHandshakeCases(t *testing.T) {
	ppp := "\x7e\xff\x03\xc0\x21\x01" // start of an LCP Configure-Request
	cases := []struct {
		name    string
		kind    Kind
		steps   []step
		mode    string
		replies string
		left    string
		dial    string
	}{
		{"ppp straight away", LAP, []step{{send: ppp}}, ModePPP, "", ppp, ""},
		{"CLIENT", SPP, []step{{send: "CLIENT"}}, ModeMSNullModem, "CLIENTSERVER", "", ""},
		{"CLIENT split", SPP, []step{{send: "CLI"}, {send: "ENT", delay: 60 * time.Millisecond}}, ModeMSNullModem, "CLIENTSERVER", "", ""},
		{"CLIENTCLIENT", SPP, []step{{send: "CLIENTCLIENT"}}, ModeMSNullModem, "CLIENTSERVERCLIENTSERVER", "", ""},
		{"CLIENT repeated later", SPP, []step{{send: "CLIENT"}, {send: "CLIENT", delay: 200 * time.Millisecond}}, ModeMSNullModem, "CLIENTSERVERCLIENTSERVER", "", ""},
		{"CLIENT then PPP", SPP, []step{{send: "CLIENT" + ppp}}, ModeMSNullModem, "CLIENTSERVER", ppp, ""},
		{"CR then CLIENT (OS 4 script)", SPP, []step{{send: "\r"}, {send: "CLIENT", delay: 50 * time.Millisecond}}, ModeMSNullModem, "CLIENTSERVER", "", ""},
		{"AT dial", DUN, []step{{send: "AT&F\r"}, {send: "ATZ\r", delay: 20 * time.Millisecond}, {send: "ATDT0\r", delay: 20 * time.Millisecond}}, ModeAT, "\r\nOK\r\n\r\nOK\r\n\r\nCONNECT 115200\r\n", "", "ATDT0"},
		{"lowercase at, CRLF", DUN, []step{{send: "ate0v1\r\n"}, {send: "atd0\r\n", delay: 20 * time.Millisecond}}, ModeAT, "\r\nOK\r\n\r\nCONNECT 115200\r\n", "", "atd0"},
		{"ATZ and ATDT0 in one read", DUN, []step{{send: "ATZ\rATDT0\r"}}, ModeAT, "\r\nOK\r\n\r\nCONNECT 115200\r\n", "", "ATDT0"},
		{"AT split mid-line", DUN, []step{{send: "AT+CGDC"}, {send: "ONT=1\rATD*99#\r", delay: 60 * time.Millisecond}}, ModeAT, "\r\nOK\r\n\r\nCONNECT 115200\r\n", "", "ATD*99#"},
		// The Palm T|X sequence from the 2026-11-09 trace, with its 3.5 s pause
		// scaled down: the pause outlasts ms_timeout_ms (400) but not
		// at_timeout_ms (1000).
		{"Palm T|X: slow first AT", DUN, []step{{send: "ATQ0V1Z\r", delay: 600 * time.Millisecond}, {send: "ATDT0\r", delay: 20 * time.Millisecond}}, ModeAT, "\r\nOK\r\n\r\nCONNECT 115200\r\n", "", "ATDT0"},
		// The full Palm T|X sequence from the second trace: the dial is the
		// last command in a compound line.
		{"Palm T|X: compound dial", DUN, []step{{send: "ATQ0V1Z\r"}, {send: "ATQ0E0\r", delay: 20 * time.Millisecond}, {send: "ATQ0E0&D0&C1V1S7=90L1DT0\r", delay: 20 * time.Millisecond}}, ModeAT, "\r\nOK\r\n\r\nOK\r\n\r\nCONNECT 115200\r\n", "", "ATQ0E0&D0&C1V1S7=90L1DT0"},
		{"&D2 is not a dial", DUN, []step{{send: "AT&D2\r"}, {send: "ATDT0\r", delay: 20 * time.Millisecond}}, ModeAT, "\r\nOK\r\n\r\nCONNECT 115200\r\n", "", "ATDT0"},
		{"ATH hangs up", DUN, []step{{send: "ATZ\r"}, {send: "ATH\r", delay: 20 * time.Millisecond}}, ModeHangup, "\r\nOK\r\n\r\nOK\r\n", "", ""},
		{"AT on SPP is not emulated", SPP, []step{{send: "ATZ\r"}}, ModeUnknown, "", "ATZ\r", ""},
		{"garbage", SPP, []step{{send: "hello"}}, ModeUnknown, "", "hello", ""},
		{"silence", SPP, nil, ModeTimeout, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, replies, left, err := runCase(t, c.kind, hsConfig(), c.steps)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if r.Mode != c.mode {
				t.Errorf("mode = %q, want %q", r.Mode, c.mode)
			}
			if string(replies) != c.replies {
				t.Errorf("replies = %q, want %q", replies, c.replies)
			}
			if string(left) != c.left {
				t.Errorf("left for pppd = %q, want %q", left, c.left)
			}
			if r.Dial != c.dial {
				t.Errorf("dial = %q, want %q", r.Dial, c.dial)
			}
		})
	}
}

func TestHandshakeDisabled(t *testing.T) {
	cfg := hsConfig()
	cfg.MSNullModem = false
	cfg.ATEmulator = false
	r, replies, left, err := runCase(t, DUN, cfg, []step{{send: "CLIENT"}})
	if err != nil || r.Mode != ModeUnknown || len(replies) != 0 || string(left) != "CLIENT" {
		t.Errorf("ms_null_modem=false: mode %q err %v replies %q left %q", r.Mode, err, replies, left)
	}
	r, replies, _, _ = runCase(t, DUN, cfg, []step{{send: "ATZ\r"}})
	if r.Mode != ModeUnknown || len(replies) != 0 {
		t.Errorf("at_emulator=false: mode %q replies %q", r.Mode, replies)
	}
}

func TestHandshakeTimeoutAndClose(t *testing.T) {
	start := time.Now()
	r, _, _, err := runCase(t, SPP, hsConfig(), nil)
	if err != nil || r.Mode != ModeTimeout {
		t.Fatalf("mode %q err %v", r.Mode, err)
	}
	if el := time.Since(start); el < 350*time.Millisecond || el > 2*time.Second {
		t.Errorf("timeout took %s, want about 400ms", el)
	}

	// DUN waits at_timeout_ms (1000), not ms_timeout_ms (400).
	start = time.Now()
	r, _, _, err = runCase(t, DUN, hsConfig(), nil)
	if err != nil || r.Mode != ModeTimeout {
		t.Fatalf("dun: mode %q err %v", r.Mode, err)
	}
	if el := time.Since(start); el < 950*time.Millisecond || el > 3*time.Second {
		t.Errorf("dun timeout took %s, want about 1s", el)
	}

	p := newPair(t)
	_ = unix.Close(p.cli)
	if _, err := Run(context.Background(), p.srv, SPP, hsConfig()); !errors.Is(err, ErrClosed) {
		t.Errorf("closed peer: err = %v, want ErrClosed", err)
	}

	p = newPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	cfg := hsConfig()
	cfg.MSTimeoutMS = 5000
	start = time.Now()
	if _, err := Run(ctx, p.srv, SPP, cfg); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Error("cancel took too long")
	}
}

func TestParseATLine(t *testing.T) {
	cases := []struct {
		line         string
		dial, hangup bool
	}{
		{"ATDT0", true, false},
		{"atd*99#", true, false},
		{"ATQ0E0&D0&C1V1S7=90L1DT0", true, false},
		{"AT S7=45 DT 555", true, false},
		{"AT+CGDCONT=1,\"IP\",\"apn\";D*99#", true, false},
		{"AT&D2", false, false},
		{"ATS0=0", false, false},
		{"ATQ0V1Z", false, false},
		{"AT+CGDCONT=1,\"IP\",\"apn\"", false, false},
		{"ATH", false, true},
		{"ATQ0H0", false, true},
		{"AT", false, false},
		{"CLIENT", false, false},
	}
	for _, c := range cases {
		dial, hangup := parseATLine(c.line)
		if dial != c.dial || hangup != c.hangup {
			t.Errorf("parseATLine(%q) = dial %v hangup %v, want %v %v", c.line, dial, hangup, c.dial, c.hangup)
		}
	}
}

// D-060: SetHandshake replaces the settings the next connection uses.
func TestSetHandshake(t *testing.T) {
	cfg := config.Default()
	s := &Server{cfg: cfg}
	if s.handshakeCfg().MSTimeoutMS != 10000 {
		t.Errorf("default ms_timeout_ms = %d, want 10000", s.handshakeCfg().MSTimeoutMS)
	}
	h := cfg.Handshake
	h.MSTimeoutMS = 4000
	s.SetHandshake(h)
	if s.handshakeCfg().MSTimeoutMS != 4000 || cfg.Handshake.MSTimeoutMS != 10000 {
		t.Errorf("after SetHandshake: %d (config %d)", s.handshakeCfg().MSTimeoutMS, cfg.Handshake.MSTimeoutMS)
	}
}
