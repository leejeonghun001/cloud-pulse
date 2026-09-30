package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLogWriter_CreatesAndWritesFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "logs", "agent.log")
	writer, err := newLogWriter(path)
	if err != nil {
		t.Fatalf("newLogWriter: %v", err)
	}
	if _, err := writer.Write([]byte("agent started\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "agent started\n" {
		t.Errorf("log = %q", got)
	}
}

func TestLogWriter_RotatesAtSizeCap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agent.log")
	writer, err := newLogWriter(path)
	if err != nil {
		t.Fatalf("newLogWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()
	rotating := writer.(*rotatingLogWriter)
	if _, err := rotating.file.Write(bytes.Repeat([]byte("x"), int(maxAgentLogBytes))); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	if _, err := writer.Write([]byte("next\n")); err != nil {
		t.Fatalf("Write after cap: %v", err)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("ReadFile rotated: %v", err)
	}
	if int64(len(rotated)) != maxAgentLogBytes {
		t.Errorf("rotated length = %d, want %d", len(rotated), maxAgentLogBytes)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile current: %v", err)
	}
	if string(current) != "next\n" {
		t.Errorf("current log = %q", current)
	}
}
