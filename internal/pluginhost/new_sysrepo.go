//go:build sysrepo

package pluginhost

// New returns CGoHost when the `sysrepo` build tag is set.
func New() Host { return NewCGoHost() }
