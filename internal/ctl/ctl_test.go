package ctl

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"palm-gw/internal/config"
)

func startServer(t *testing.T) *Server {
	t.Helper()
	// Unix socket paths are limited to ~104 bytes on macOS and t.TempDir()
	// embeds the test name, so use a short temp directory instead.
	dir, err := os.MkdirTemp("", "pgw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	s := NewServer(path, "nonexistent-group-for-test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.Serve(ctx)
	t.Cleanup(func() { cancel(); s.Close() })
	return s
}

func TestStatusRoundTrip(t *testing.T) {
	s := startServer(t)
	started := time.Now().Add(-90 * time.Second)
	cfg := config.Default()
	cfg.Bluetooth.PIN = "4821"
	s.Handle("status", func(_ context.Context, _ Request, w *Writer) error {
		return w.Send(StatusReply{
			Version: "test", Phase: "0", StartedAt: started,
			UptimeSeconds: int64(time.Since(started).Seconds()),
			ConfigPath:    "/etc/palm-gw/config.toml",
			Config:        cfg.Redacted(),
		})
	})

	var st StatusReply
	if err := Call(s.Path(), Request{Cmd: "status"}, &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != "test" || st.Config == nil || st.Config.Network.Gateway != "10.42.0.1" {
		t.Fatalf("bad reply: %+v", st)
	}
	if st.Config.Bluetooth.PIN != "(redacted)" {
		t.Fatalf("PIN leaked over the socket: %q", st.Config.Bluetooth.PIN)
	}
	if st.UptimeSeconds < 89 {
		t.Fatalf("uptime: %d", st.UptimeSeconds)
	}
}

func TestUnknownCommandAndHandlerError(t *testing.T) {
	s := startServer(t)
	s.Handle("boom", func(context.Context, Request, *Writer) error { return errors.New("kaboom") })

	var rerr *RemoteError
	err := Call(s.Path(), Request{Cmd: "nope"}, &OKReply{})
	if !errors.As(err, &rerr) {
		t.Fatalf("expected RemoteError, got %v", err)
	}
	err = Call(s.Path(), Request{Cmd: "boom"}, &OKReply{})
	if !errors.As(err, &rerr) || rerr.Msg != "kaboom" {
		t.Fatalf("expected kaboom, got %v", err)
	}
}

func TestStreamingCancelledOnDisconnect(t *testing.T) {
	s := startServer(t)
	done := make(chan struct{})
	s.Handle("events", func(ctx context.Context, _ Request, w *Writer) error {
		defer close(done)
		_ = w.Send(map[string]string{"event": "hello"})
		<-ctx.Done()
		return nil
	})
	conn, err := Dial(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Send(Request{Cmd: "events"}); err != nil {
		t.Fatal(err)
	}
	var ev map[string]string
	if err := conn.Next(&ev); err != nil || ev["event"] != "hello" {
		t.Fatalf("first event: %v %v", ev, err)
	}
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler was not cancelled when the client disconnected")
	}
}

func TestBadJSON(t *testing.T) {
	s := startServer(t)
	conn, err := Dial(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.c.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}
	var rerr *RemoteError
	if err := conn.Next(nil); !errors.As(err, &rerr) {
		t.Fatalf("expected RemoteError, got %v", err)
	}
}

func TestDialMissingSocket(t *testing.T) {
	_, err := Dial(filepath.Join(t.TempDir(), "none.sock"))
	if err == nil {
		t.Fatal("expected error")
	}
}
