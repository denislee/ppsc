// Package logging configures the application's structured logger (slog),
// fanning output to both the console and a persistent log file so runs can be
// reviewed and troubleshooted after the fact.
package logging

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
)

// Setup installs a slog default logger writing to stderr and, if path is
// non-empty, appending to that file as well. It returns a closer for the file
// (a no-op when logging to stderr only). The standard log package is also
// routed through the same writer so any stray log.* calls are captured.
func Setup(path string, debug bool) (io.Closer, error) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	out := io.Writer(os.Stderr)
	var closer io.Closer = io.NopCloser(nil)

	if path != "" {
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		out = io.MultiWriter(os.Stderr, f)
		closer = f
	}

	handler := slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// Capture anything still using the standard logger (e.g. http.Server).
	log.SetOutput(out)
	log.SetFlags(0)

	return closer, nil
}
