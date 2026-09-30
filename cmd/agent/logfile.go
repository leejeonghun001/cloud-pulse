package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// maxAgentLogBytes caps a single agent/updater service log at 5 MiB. The
// previous file is retained as .1, keeping Windows service diagnostics useful
// without allowing an unavailable hub or a repeated update failure to consume
// ProgramData indefinitely.
const maxAgentLogBytes int64 = 5 << 20

type rotatingLogWriter struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func newLogWriter(path string) (io.WriteCloser, error) {
	if path == "" {
		return nopWriteCloser{Writer: os.Stderr}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return &rotatingLogWriter{path: path, file: file}, nil
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if info, err := w.file.Stat(); err == nil && info.Size()+int64(len(p)) > maxAgentLogBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

func (w *rotatingLogWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close log before rotation: %w", err)
	}
	_ = os.Remove(w.path + ".1")
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate log: %w", err)
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open rotated log: %w", err)
	}
	w.file = file
	return nil
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
