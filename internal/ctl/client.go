package ctl

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// RemoteError is an {"error":...} line from the daemon.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }

// Conn is one client connection carrying one command.
type Conn struct {
	c net.Conn
	r *bufio.Reader
}

// ErrNotRunning means there is no control socket: palm-gwd is not running.
var ErrNotRunning = errors.New("palm-gwd is not running")

// Dial connects to the control socket.
func Dial(path string) (*Conn, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w (no socket at %s)", ErrNotRunning, path)
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("permission denied on %s: add your user to the palm-gw group or use sudo", path)
		}
		return nil, fmt.Errorf("connect to %s: %w", path, err)
	}
	return &Conn{c: c, r: bufio.NewReaderSize(c, 64*1024)}, nil
}

// Send writes the request line.
func (c *Conn) Send(req Request) error {
	return json.NewEncoder(c.c).Encode(req)
}

// Next reads the next reply line into v. It returns *RemoteError for an
// error line and io.EOF when the daemon has closed the connection.
func (c *Conn) Next(v any) error {
	line, err := readLine(c.r)
	if err != nil {
		return err
	}
	var probe struct {
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(line, &probe); err == nil && probe.Error != nil {
		return &RemoteError{Msg: *probe.Error}
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(line, v)
}

// NextRaw reads the next reply line as raw JSON.
func (c *Conn) NextRaw() (json.RawMessage, error) {
	line, err := readLine(c.r)
	if err != nil {
		return nil, err
	}
	var probe struct {
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(line, &probe); err == nil && probe.Error != nil {
		return nil, &RemoteError{Msg: *probe.Error}
	}
	return json.RawMessage(append([]byte(nil), line...)), nil
}

// SetDeadline bounds the next read or write.
func (c *Conn) SetDeadline(t time.Time) error { return c.c.SetDeadline(t) }

// Close closes the connection, which cancels a streaming command.
func (c *Conn) Close() error { return c.c.Close() }

// Call sends one request and decodes the single reply into reply.
func Call(path string, req Request, reply any) error {
	conn, err := Dial(path)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := conn.Send(req); err != nil {
		return err
	}
	if err := conn.Next(reply); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("daemon closed the connection without replying")
		}
		return err
	}
	return nil
}
