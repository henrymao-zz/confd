package pluginhost

import (
	"sync"

	"github.com/example/confd/internal/sysrepoadapter"
)

// MockHost is a pure-Go Host for tests. It records which specs were
// Start'ed and in what order, without touching dlopen or sysrepo.
//
// A spec whose Name starts with "fail-" is treated as an init failure
// (disabled, logged, skipped), matching the real host's behavior.
type MockHost struct {
	mu      sync.Mutex
	loaded  []string // names of successfully loaded plugins, in load order
	started bool
}

// NewMockHost returns an empty MockHost.
func NewMockHost() *MockHost { return &MockHost{} }

// Start processes specs sequentially. Plugins whose Name begins with
// "fail-" are disabled (init failure); the rest are recorded as loaded.
func (h *MockHost) Start(_ sysrepoadapter.Conn, specs []Spec) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = true
	for _, s := range specs {
		if len(s.Name) >= 5 && s.Name[:5] == "fail-" {
			continue // init failure → disabled, skipped
		}
		h.loaded = append(h.loaded, s.Name)
	}
	return nil
}

// Stop clears the loaded list (simulates reverse-order cleanup).
func (h *MockHost) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	// reverse order
	for i := len(h.loaded)/2 - 1; i >= 0; i-- {
		opp := len(h.loaded) - 1 - i
		h.loaded[i], h.loaded[opp] = h.loaded[opp], h.loaded[i]
	}
	h.loaded = nil
	return nil
}

// Names returns the names of successfully loaded plugins.
func (h *MockHost) Names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.loaded))
	copy(out, h.loaded)
	return out
}

// Started reports whether Start was called.
func (h *MockHost) Started() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started
}

var _ Host = (*MockHost)(nil)
