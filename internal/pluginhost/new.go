//go:build !sysrepo

package pluginhost

// New returns the default Host for the current build: NoopHost when
// the `sysrepo` build tag is not set (pure Go), CGoHost when it is.
func New() Host { return NoopHost{} }
