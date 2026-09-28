package config

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// LookupFunc matches os.LookupEnv.
type LookupFunc func(key string) (string, bool)

// getString returns the trimmed value of key from l, or def if key is
// unset or empty after trimming.
func getString(l LookupFunc, key, def string) string {
	v, ok := l(key)
	if !ok {
		return def
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	return v
}

// getDuration parses key as a time.Duration, returning def if unset or
// empty. It errors if the parsed value is less than min.
func getDuration(l LookupFunc, key string, def, min time.Duration) (time.Duration, error) {
	v, ok := l(key)
	if !ok {
		return def, nil
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	if d < min {
		return 0, fmt.Errorf("config: %s must be >= %s", key, min)
	}
	return d, nil
}

// getBool parses key as a bool (strconv.ParseBool), returning def if unset
// or empty.
func getBool(l LookupFunc, key string, def bool) (bool, error) {
	v, ok := l(key)
	if !ok {
		return def, nil
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s: %w", key, err)
	}
	return b, nil
}

// getInt parses key as an int, returning def if unset or empty.
func getInt(l LookupFunc, key string, def int) (int, error) {
	v, ok := l(key)
	if !ok {
		return def, nil
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return n, nil
}

// getFloat parses key as a float64, returning def if unset or empty.
func getFloat(l LookupFunc, key string, def float64) (float64, error) {
	v, ok := l(key)
	if !ok {
		return def, nil
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return f, nil
}

// splitList splits s on commas, trims whitespace from each element, and
// drops empty elements. It returns nil if no non-empty elements remain.
func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NewLogger builds a slog.Logger writing to w. level is one of
// debug|info|warn|error (case-insensitive); format is text|json
// (case-insensitive).
func NewLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "info", "":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("config: invalid log level %q", level)
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text", "":
		handler = slog.NewTextHandler(w, opts)
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		return nil, fmt.Errorf("config: invalid log format %q", format)
	}

	return slog.New(handler), nil
}
