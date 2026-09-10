// Package pluginhost implements the sysrepo-plugind replacement: it
// dlopen's the shipped libsrplg-*.so artifacts, gives each a
// sr_session_start, calls sr_plugin_init_cb (which starts the plugin's
// own event loop), and on shutdown calls sr_plugin_cleanup_cb in reverse
// load order.
//
// This is the "single daemon" piece: confd absorbs sysrepo-plugind's
// job (load + lifecycle plugins) into the same process that serves
// NETCONF.
//
// Interface:
//
//	type Host interface {
//	    Start(conn sysrepoadapter.Conn, specs []Spec) error
//	    Stop() error
//	    Names() []string
//	}
//
// Implementations:
//
//   - MockHost  — in-process mock for tests (pure Go).
//   - NoopHost  — no-op host for the default build (pure Go, always
//     available).
//   - CGoHost   — real dlopen-based host (behind the `sysrepo` build tag;
//     requires libsysrepo + dlfcn).
package pluginhost

import (
	"fmt"

	"github.com/example/confd/internal/sysrepoadapter"
)

// Spec describes one plugin to load.
type Spec struct {
	Name string // "ietf-system"
	Path string // /usr/lib/confd/plugins/libsrplg-ietf-system.so
}

// Host owns the lifecycle of a set of sysrepo plugins. It replaces
// sysrepo-plugind: dlopen → sr_session_start → sr_plugin_init_cb on
// Start; sr_plugin_cleanup_cb → sr_session_stop → dlclose on Stop.
type Host interface {
	// Start loads and initializes all specs against the given connection.
	// Plugins whose init returns non-zero are disabled and logged; the
	// rest continue.
	Start(conn sysrepoadapter.Conn, specs []Spec) error
	// Stop cleans up all loaded plugins in reverse load order, then
	// releases their sessions and dlopen handles.
	Stop() error
	// Names returns the names of successfully loaded plugins.
	Names() []string
}

// ErrNotAvailable is returned by NoopHost.Start when the plugin host
// is not built (no `sysrepo` tag).
var ErrNotAvailable = fmt.Errorf("pluginhost: not available (rebuild with -tags sysrepo)")

// NoopHost is a Host that does nothing. It is the default when the
// `sysrepo` build tag is not set.
type NoopHost struct{}

// Start returns ErrNotAvailable.
func (NoopHost) Start(_ sysrepoadapter.Conn, _ []Spec) error { return ErrNotAvailable }

// Stop does nothing.
func (NoopHost) Stop() error { return nil }

// Names returns nil.
func (NoopHost) Names() []string { return nil }

// Compile-time check.
var _ Host = NoopHost{}
