package storageusage

import (
	"net/http"
	"sort"
	"sync"
)

// Registry holds the Providers available to one storage-usage runtime.
// Registries are intentionally instance-owned: a hub, CLI invocation, or test
// can configure its own providers without changing another concurrent runtime.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry constructs an empty provider registry and registers providers.
// When more than one provider has the same ID, the last one wins.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

// DefaultRegistry constructs a registry with the production Google Drive and
// Dropbox providers. It returns a fresh registry on every call, never shared
// mutable package state.
func DefaultRegistry() *Registry {
	return NewRegistry(
		NewGoogleDriveProvider(http.DefaultClient, SSRFOptions{}),
		NewDropboxProvider(http.DefaultClient, SSRFOptions{}),
	)
}

// Register adds p to r, keyed by p.ID(). Calling Register twice with the same
// ID replaces the previous provider.
func (r *Registry) Register(p Provider) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]Provider)
	}
	r.providers[string(p.ID())] = p
}

// Get returns r's Provider for id, or (nil, false) when none is registered.
func (r *Registry) Get(id string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// All returns every registered provider sorted by provider ID.
func (r *Registry) All() []Provider {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	providers := make([]Provider, 0, len(ids))
	for _, id := range ids {
		providers = append(providers, r.providers[id])
	}
	return providers
}
