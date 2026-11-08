// Package logging builds the daemon's slog logger from the [logging] table:
// JSON (default) or text, to stderr, collected by journald.
package logging

import (
	"io"
	"log/slog"

	"palm-gw/internal/config"
)

// New returns a logger for the configured level and format writing to w.
func New(cfg config.Logging, w io.Writer) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}
