// Package models contains pure domain types and pure domain functions shared
// across cloud-pulse. It imports only the standard library.
package models

import "errors"

// ErrNotFound is the sentinel error returned by storage and mapped to HTTP
// 404 by internal/hub.
var ErrNotFound = errors.New("not found")
