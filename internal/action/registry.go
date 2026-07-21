package action

import (
	"fmt"
	"sync"
)

// Registry holds registered action types. Thread-safe for concurrent reads
// after initial registration (which must happen at init time, before
// BuildConsumers is called).
type Registry struct {
	mu    sync.RWMutex
	types map[string]Type
}

// DefaultRegistry is the process-global action type registry.
var DefaultRegistry = NewRegistry()

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{types: make(map[string]Type)}
}

// Register adds a Type to the registry. Panics on duplicate names (programming
// error — types are registered at package init time).
func (r *Registry) Register(t Type) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if _, exists := r.types[name]; exists {
		panic(fmt.Sprintf("action: duplicate type registration: %q", name))
	}
	r.types[name] = t
}

// Lookup returns the Type for name, or nil if not registered.
func (r *Registry) Lookup(name string) Type {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.types[name]
}

// Names returns all registered type names (unordered).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.types))
	for n := range r.types {
		names = append(names, n)
	}
	return names
}
