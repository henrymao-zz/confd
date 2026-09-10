// Package config parses confd's runtime configuration (YAML/CLI flags).
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config is the runtime configuration of confd.
type Config struct {
	// SSHBind is the address to listen for NETCONF-over-SSH (default :830).
	SSHBind string `yaml:"ssh_bind"`
	// SSHHostKey is the path to the SSH host key (empty = ephemeral).
	SSHHostKey string `yaml:"ssh_host_key"`
	// SSHPassword enables password auth with this password (empty = noauth).
	SSHPassword string `yaml:"ssh_password"`
	// YANGPaths are directories to load YANG modules from.
	YANGPaths []string `yaml:"yang_paths"`
	// Adapter selects the sysrepo backend: "mock" or "sysrepo".
	Adapter string `yaml:"adapter"`
	// SysrepoSocket is the path to the sysrepo socket (for the sysrepo adapter).
	SysrepoSocket string `yaml:"sysrepo_socket"`
	// PluginsDir is the directory containing libsrplg-*.so plugin
	// artifacts (for the sysrepo adapter). Empty = no plugins.
	PluginsDir string `yaml:"plugins_dir"`
	// Plugins is an allowlist of plugin names to load. Empty = load all
	// *.so files in PluginsDir.
	Plugins []string `yaml:"plugins"`
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		SSHBind:    "0.0.0.0:830",
		YANGPaths:  []string{"/etc/confd/yang", "/usr/share/yang/modules"},
		Adapter:    "mock",
	}
}

// FromFlags applies CLI flag overrides to a base config.
func FromFlags(base Config, args []string) (Config, error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--bind="):
			base.SSHBind = strings.TrimPrefix(a, "--bind=")
		case a == "--bind" && i+1 < len(args):
			i++
			base.SSHBind = args[i]
		case strings.HasPrefix(a, "--host-key="):
			base.SSHHostKey = strings.TrimPrefix(a, "--host-key=")
		case strings.HasPrefix(a, "--password="):
			base.SSHPassword = strings.TrimPrefix(a, "--password=")
		case strings.HasPrefix(a, "--yang-path="):
			base.YANGPaths = append(base.YANGPaths, strings.TrimPrefix(a, "--yang-path="))
		case strings.HasPrefix(a, "--adapter="):
			base.Adapter = strings.TrimPrefix(a, "--adapter=")
		case strings.HasPrefix(a, "--sysrepo-socket="):
			base.SysrepoSocket = strings.TrimPrefix(a, "--sysrepo-socket=")
		case strings.HasPrefix(a, "--plugins-dir="):
			base.PluginsDir = strings.TrimPrefix(a, "--plugins-dir=")
		case strings.HasPrefix(a, "--plugin="):
			base.Plugins = append(base.Plugins, strings.TrimPrefix(a, "--plugin="))
		case a == "-h", a == "--help":
			fmt.Fprintln(os.Stderr, usage())
			os.Exit(0)
		default:
			return base, fmt.Errorf("unknown flag: %s", a)
		}
	}
	return base, nil
}

func usage() string {
	return `confd - Go-based NETCONF server

Usage:
  confd serve [flags]

Flags:
  --bind=<addr>          SSH listen address (default 0.0.0.0:830)
  --host-key=<path>      SSH host key path (default: ephemeral)
  --password=<pw>        Enable SSH password auth
  --yang-path=<dir>      Add a YANG search directory (repeatable)
  --adapter=<mock|sysrepo>  Select backend (default: mock)
  --sysrepo-socket=<path>   sysrepo socket (for --adapter=sysrepo)
  --plugins-dir=<dir>    Directory with libsrplg-*.so (for --adapter=sysrepo)
  --plugin=<name>        Allowlist a plugin (repeatable; empty = all)
`
}
