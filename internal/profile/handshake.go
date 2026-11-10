package profile

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"palm-gw/internal/config"
)

// Handshake modes, as logged in the `handshake` event and shown in status.
const (
	ModeMSNullModem = "ms_null_modem" // answered CLIENT with CLIENTSERVER
	ModePPP         = "none"          // first byte was a PPP flag
	ModeAT          = "at"            // AT emulator answered a dial
	ModeTimeout     = "timeout"       // nothing recognisable; pppd starts blind
	ModeUnknown     = "unknown"       // unrecognised bytes; left for pppd
	ModeHangup      = "hangup"        // ATH: close the link, no pppd
)

// ErrClosed means the peer closed the link during the handshake.
var ErrClosed = errors.New("handshake: peer closed the link")

// Result says what the handshake saw.
type Result struct {
	Mode       string `json:"mode"`
	Clients    int    `json:"clients,omitempty"` // CLIENT strings answered
	ATCommands int    `json:"at_commands,omitempty"`
	Dial       string `json:"dial,omitempty"`        // the ATD line
	FirstBytes string `json:"first_bytes,omitempty"` // hex, for unknown input
}

const (
	pollSlice       = 100 * time.Millisecond
	partialWait     = 20 * time.Millisecond
	afterClientWait = 1500 * time.Millisecond
	afterATWait     = 5000 * time.Millisecond
)

var (
	client       = []byte("CLIENT")
	clientServer = []byte("CLIENTSERVER")
)

// Run performs the pre-PPP handshake on fd (PLAN.md section 8.4). It peeks
// with MSG_PEEK and consumes only what it answers, so a PPP frame that
// arrives straight after CLIENT stays in the socket for pppd. ctx cancels it
// (a newer connection from the same device replaced this one).
func Run(ctx context.Context, fd int, k Kind, cfg config.Handshake) (Result, error) {
	res := Result{Mode: ModeTimeout}
	timeout := time.Duration(cfg.MSTimeoutMS) * time.Millisecond
	if cfg.ATEmulator && k == DUN {
		// A DUN terminal always speaks first, and not quickly: a Palm T|X
		// sends its first AT command 3.5 s after the channel opens
		// (DECISIONS.md D-023). Starting pppd blind before then makes the
		// Palm report "modem not found".
		timeout = time.Duration(cfg.ATTimeoutMS) * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 64)
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return res, nil
		}
		ready, err := waitReadable(fd, min(remain, pollSlice))
		if err != nil {
			return res, err
		}
		if !ready {
			continue
		}
		n, _, err := unix.Recvfrom(fd, buf, unix.MSG_PEEK)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return res, err
		}
		if n == 0 {
			return res, ErrClosed
		}
		b := buf[:n]

		// Stray line endings: the Palm OS 4 login script sends a CR before
		// CLIENT, and AT commands may end in CRLF.
		if skip := leadingNewlines(b); skip > 0 {
			if err := consume(fd, skip); err != nil {
				return res, err
			}
			continue
		}

		if b[0] == 0x7E { // Case B: PPP straight away
			if res.Mode == ModeTimeout {
				res.Mode = ModePPP
			}
			return res, nil
		}

		if cfg.MSNullModem { // Case A
			if bytes.HasPrefix(b, client) {
				if err := consume(fd, len(client)); err != nil {
					return res, err
				}
				if err := writeAll(fd, clientServer); err != nil {
					return res, err
				}
				res.Mode = ModeMSNullModem
				res.Clients++
				deadline = time.Now().Add(afterClientWait)
				continue
			}
			if bytes.HasPrefix(client, b) { // CLIENT split across writes
				time.Sleep(partialWait)
				continue
			}
		}

		if cfg.ATEmulator && k == DUN && (b[0] == 'A' || b[0] == 'a') { // Case C
			if n == 1 {
				time.Sleep(partialWait)
				continue
			}
			if b[1] == 'T' || b[1] == 't' {
				end := bytes.IndexByte(b, '\r')
				if end < 0 && n < len(buf) {
					time.Sleep(partialWait) // line not complete yet
					continue
				}
				take := end + 1
				if end < 0 {
					end, take = n, n // 64 bytes and no CR: treat it as a line
				} else if take < n && b[take] == '\n' {
					take++ // CRLF: take the LF too, so none is left for pppd
				}
				line := strings.TrimSpace(string(b[:end]))
				if err := consume(fd, take); err != nil {
					return res, err
				}
				res.ATCommands++
				switch dial, hangup := parseATLine(line); {
				case dial:
					res.Mode = ModeAT
					res.Dial = line
					return res, writeAll(fd, []byte("\r\n"+cfg.ATConnectBanner+"\r\n"))
				case hangup:
					res.Mode = ModeHangup
					return res, writeAll(fd, []byte("\r\nOK\r\n"))
				}
				if err := writeAll(fd, []byte("\r\nOK\r\n")); err != nil {
					return res, err
				}
				deadline = time.Now().Add(afterATWait)
				continue
			}
		}

		// Anything else: leave it for pppd, which will log LCP garbage if it
		// is not PPP.
		res.FirstBytes = hex.EncodeToString(b)
		if res.Mode == ModeTimeout {
			res.Mode = ModeUnknown
		}
		return res, nil
	}
}

// parseATLine walks one AT command line, which can hold several commands
// (V.250 section 5.2): a Palm T|X dials with "ATQ0E0&D0&C1V1S7=90L1DT0",
// so the dial is not always at the start (DECISIONS.md D-024). It reports
// whether the line dials (a D command, which takes the rest of the line)
// or hangs up (an H command and no dial). "&D0" and "S7=90" are settings,
// not a dial.
func parseATLine(line string) (dial, hangup bool) {
	s := strings.ToUpper(strings.TrimSpace(line))
	if !strings.HasPrefix(s, "AT") {
		return false, false
	}
	s = s[2:]
	digits := func(i int) int {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i
	}
	for i := 0; i < len(s); {
		switch s[i] {
		case 'D':
			return true, false
		case 'H':
			hangup = true
			i = digits(i + 1)
		case '&', '\\', '%':
			i = digits(i + 2) // prefixed basic command, e.g. &D0, \N3, %C0
		case '+', '*', '$', '^':
			// Extended command: runs to the next ';' or the end of the line.
			end := strings.IndexByte(s[i:], ';')
			if end < 0 {
				return false, hangup
			}
			i += end + 1
		case 'S':
			i = digits(i + 1)
			if i < len(s) && s[i] == '=' {
				i = digits(i + 1)
			} else if i < len(s) && s[i] == '?' {
				i++
			}
		default:
			i = digits(i + 1) // E0, V1, Q0, Z, L1, spaces, '?'
		}
	}
	return false, hangup
}

func leadingNewlines(b []byte) int {
	i := 0
	for i < len(b) && (b[i] == '\r' || b[i] == '\n') {
		i++
	}
	return i
}

func waitReadable(fd int, d time.Duration) (bool, error) {
	ms := int(d / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(pfd, ms)
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if pfd[0].Revents&unix.POLLIN == 0 && pfd[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
		return false, ErrClosed
	}
	return true, nil
}

// consume reads and discards exactly n bytes that a peek showed are there.
func consume(fd, n int) error {
	tmp := make([]byte, n)
	for got := 0; got < n; {
		m, err := unix.Read(fd, tmp[got:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if m == 0 {
			return ErrClosed
		}
		got += m
	}
	return nil
}

func writeAll(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := unix.Write(fd, b)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}
