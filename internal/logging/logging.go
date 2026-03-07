// Package logging provides structured JSON logging setup built on Go's log/slog.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// ParseLevel converts a string log level to slog.Level.
// Matching is case-insensitive. Unrecognized values default to slog.LevelInfo.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Setup initializes a JSON slog logger writing to w at the given level.
// It also sets the logger as the global slog default so package-level
// slog calls (slog.Info, slog.Error, etc.) use the same handler.
//
// Per research pitfall 4: always set the handler explicitly at startup.
func Setup(w io.Writer, level string) *slog.Logger {
	lvl := ParseLevel(level)
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: lvl,
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}
