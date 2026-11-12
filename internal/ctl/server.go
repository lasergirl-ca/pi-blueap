package ctl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
)

// maxLine bounds a request line so a misbehaving client cannot exhaust memory.
const maxLine = 1 << 20

// Handler serves one command. It writes replies through w; returning a
// non-nil error sends {"error":...} to the client. ctx is cancelled when the
// client disconnects or the server shuts down.
type Handler func(ctx context.Context, req Request, w *Writer) error

// Writer serialises JSON lines to one client connection.
type Writer struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// Send writes v as one JSON line.
func (w *Writer) Send(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(v)
}

// Server owns the control socket.
type Server struct {
	path     string
	group    string
	log      *slog.Logger
	handlers map[string]Handler
	ln       net.Listener
	wg       sync.WaitGroup
}

// NewServer prepares a server for the socket path; the socket is created by
// Listen. group names the Unix group that may connect (socket mode 0660).
func NewServer(path, group string, log *slog.Logger) *Server {
	return &Server{
		path:     path,
		group:    group,
		log:      log.With("component", "ctl"),
		handlers: map[string]Handler{},
	}
}

// Handle registers the handler for a command name.
func (s *Server) Handle(cmd string, h Handler) { s.handlers[cmd] = h }

// Listen creates the socket: parent directory 0750 root:<group>, stale socket
// removed, socket mode 0660 root:<group>, when the group exists on this host.
// systemd's RuntimeDirectory= creates the directory root:root, which would
// stop group members from reaching the socket at all, so the directory's
// group is set here too.
func (s *Server) Listen() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("ctl: %w", err)
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("ctl: remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("ctl: %w", err)
	}
	if err := os.Chmod(s.path, 0o660); err != nil {
		_ = ln.Close()
		return fmt.Errorf("ctl: chmod: %w", err)
	}
	if g, err := user.LookupGroup(s.group); err == nil {
		gid, _ := strconv.Atoi(g.Gid)
		if err := os.Chown(s.path, -1, gid); err != nil {
			s.log.Warn("ctl socket group not applied", "group", s.group, "err", err)
		}
		if err := os.Chown(dir, -1, gid); err != nil {
			s.log.Warn("ctl directory group not applied", "dir", dir, "group", s.group, "err", err)
		} else if err := os.Chmod(dir, 0o750); err != nil {
			s.log.Warn("ctl directory mode not applied", "dir", dir, "err", err)
		}
	} else {
		s.log.Warn("ctl group does not exist; socket is root-only", "group", s.group)
	}
	s.ln = ln
	s.log.Info("ctl socket listening", "path", s.path)
	return nil
}

// Serve accepts connections until ctx is done or Close is called.
func (s *Server) Serve(ctx context.Context) {
	go func() {
		<-ctx.Done()
		if s.ln != nil {
			_ = s.ln.Close()
		}
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Warn("ctl accept", "err", err)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(ctx, conn)
		}()
	}
}

// Close stops accepting, waits for in-flight commands and removes the socket.
func (s *Server) Close() {
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.wg.Wait()
	_ = os.Remove(s.path)
}

// Path returns the socket path.
func (s *Server) Path() string { return s.path }

// ErrUnknownCommand is returned by Call for a command no handler serves.
var ErrUnknownCommand = errors.New("unknown command")

// Call runs a command in-process, exactly as a socket client's request would
// run, and hands each reply line to emit. The web UI uses it, so it offers
// what palm-gwctl offers and nothing else (PLAN.md section 15). A handler's
// error comes back as the error; an error from emit ends the command the way
// a disconnecting client does.
func (s *Server) Call(ctx context.Context, req Request, emit func(line json.RawMessage) error) error {
	h, ok := s.handlers[req.Cmd]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownCommand, req.Cmd)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &Writer{enc: json.NewEncoder(lineFunc(func(p []byte) error {
		line := make(json.RawMessage, len(p))
		copy(line, p)
		if err := emit(bytes.TrimRight(line, "\n")); err != nil {
			cancel()
			return err
		}
		return nil
	}))}
	return h(ctx, req, w)
}

// lineFunc is an io.Writer for json.Encoder, which writes each value, newline
// included, in one Write.
type lineFunc func(p []byte) error

func (f lineFunc) Write(p []byte) (int, error) {
	if err := f(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) serveConn(parent context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	w := &Writer{enc: json.NewEncoder(conn)}

	r := bufio.NewReaderSize(conn, 64*1024)
	line, err := readLine(r)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.log.Debug("ctl read", "err", err)
		}
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = w.Send(ErrorReply{Error: "bad request: " + err.Error()})
		return
	}
	h, ok := s.handlers[req.Cmd]
	if !ok {
		_ = w.Send(ErrorReply{Error: fmt.Sprintf("unknown command %q", req.Cmd)})
		return
	}

	// Cancel the command when the client goes away: any further read on the
	// connection returning (EOF or error) means it disconnected.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		_, _ = r.ReadByte()
		cancel()
	}()

	if err := h(ctx, req, w); err != nil {
		_ = w.Send(ErrorReply{Error: err.Error()})
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, errors.New("request too long")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}
