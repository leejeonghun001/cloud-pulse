package listen

import (
	"bytes"
	"log/slog"
)

// testLogger returns a slog.Logger that discards output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
