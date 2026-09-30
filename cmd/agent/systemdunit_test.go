package main

import (
	"context"
	"reflect"
	"testing"
)

func TestEnableAndStartUpdatePathUnit(t *testing.T) {
	t.Parallel()
	var gotName string
	var gotArgs []string
	err := enableAndStartUpdatePathUnit(context.Background(), func(_ context.Context, name string, args ...string) error {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil
	})
	if err != nil {
		t.Fatalf("enableAndStartUpdatePathUnit: %v", err)
	}
	if gotName != "systemctl" {
		t.Errorf("command = %q, want systemctl", gotName)
	}
	want := []string{"enable", "--now", "cloud-pulse-agent-update.path"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %q, want %q", gotArgs, want)
	}
}
