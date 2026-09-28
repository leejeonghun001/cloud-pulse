package models

import (
	"errors"
	"testing"
)

func TestErrNotFound(t *testing.T) {
	t.Parallel()

	if ErrNotFound == nil {
		t.Fatal("ErrNotFound must not be nil")
	}
	if ErrNotFound.Error() != "not found" {
		t.Errorf("ErrNotFound.Error() = %q, want %q", ErrNotFound.Error(), "not found")
	}

	wrapped := errors.New("wrapper: " + ErrNotFound.Error())
	if errors.Is(wrapped, ErrNotFound) {
		t.Error("a freshly constructed error must not match ErrNotFound via errors.Is")
	}
}
