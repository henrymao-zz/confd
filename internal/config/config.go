// Package config parses confd's runtime configuration from a YAML file
// and/or CLI flags. CLI flags override YAML values, which override
// compiled-in defaults.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/example/confd/internal/yangprov"
	"gopkg.in/yaml.v3"
)

// Config is the runtime configuration of confd.
type Config struct {
	SSH SSHConfig `yaml:"ssh"`

	// Adapter selects the sysrepo backend: "mock" or "sysrepo".
	Adapter string `yaml:"adapter"`
	// SysrepoSocket is the path to the sysrepo socket (for the sysrepo adapter).
	SysrepoSocket string `yaml:"sysrepo_socket"`
	// SysrepoDir is the repository path for sysrepo persistent files.
	// If set, overrides the default /etc/sysrepo via SYSREPO_REPOSITORY_PATH env.
	SysrepoDir string `yaml:"sysrepo_dir"`

	// Plugins configures the plugin host and YANG provisioning.
	// Plugin .so files are discovered from Dir; YANG modules and features
	// are provisioned from each Entry's yang_dir/modules/features.
	Plugins PluginsConfig `yaml:"plugins"`
}

// SSHConfig configures the SSH listener.
type SSHConfig struct {
	Bind     string `yaml:"bind"`
	HostKey  string `yaml:"host_key"`
	Password string `yaml:"password"`
}

// PluginsConfig configures the plugin host and YANG provisioning.
type PluginsConfig struct {
	// Dir is the directory containing libsrplg-*.so plugin artifacts.
	Dir string `yaml:"dir"`
	// Entries is the list of plugins to load with their YANG provisioning
	// specs. Each entry has a name (matched to libsrplg-<name>.so),
	// a yang_dir, modules list, and optional features.
	Entries []yangprov.PluginSpec `yaml:"entries"`
	// Names is an allowlist of plugin names (empty = load all entries).
	// If non-empty, only entries whose Name matches are loaded.
	Names []string `yaml:"names"`
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		SSH: SSHConfig{
			Bind: "0.0.0.0:830",
		},
		Adapter: "sysrepo",
	}
}

// DefaultConfigPath is the default path for the YAML config file.
const DefaultConfigPath = "/etc/confd/confd.yaml"

// LoadConfig loads a YAML config file and returns a Config. If the file
// does not exist, returns Default() with no error.
func LoadConfig(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultConfigPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// FromFlags applies CLI flag overrides to a base config (from YAML).
func FromFlags(base Config, args []string) (Config, error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--config="):
			path := strings.TrimPrefix(a, "--config=")
			loaded, err := LoadConfig(path)
			if err != nil {
				return base, err
			}
			base = loaded
		case a == "--config" && i+1 < len(args):
			i++
			loaded, err := LoadConfig(args[i])
			if err != nil {
				return base, err
			}
			base = loaded
		case strings.HasPrefix(a, "--bind="):
			base.SSH.Bind = strings.TrimPrefix(a, "--bind=")
		case a == "--bind" && i+1 < len(args):
			i++
			base.SSH.Bind = args[i]
		case strings.HasPrefix(a, "--host-key="):
			base.SSH.HostKey = strings.TrimPrefix(a, "--host-key=")
		case strings.HasPrefix(a, "--password="):
			base.SSH.Password = strings.TrimPrefix(a, "--password=")
		case strings.HasPrefix(a, "--adapter="):
			base.Adapter = strings.TrimPrefix(a, "--adapter=")
		case strings.HasPrefix(a, "--sysrepo-socket="):
			base.SysrepoSocket = strings.TrimPrefix(a, "--sysrepo-socket=")
		case strings.HasPrefix(a, "--sysrepo-dir="):
			base.SysrepoDir = strings.TrimPrefix(a, "--sysrepo-dir=")
		case strings.HasPrefix(a, "--plugins-dir="):
			base.Plugins.Dir = strings.TrimPrefix(a, "--plugins-dir=")
		case strings.HasPrefix(a, "--plugin="):
			base.Plugins.Names = append(base.Plugins.Names, strings.TrimPrefix(a, "--plugin="))
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
  confd schema-list [flags]

Flags:
  --config=<path>        Path to YAML config file (default: /etc/confd/confd.yaml)
  --bind=<addr>          SSH listen address (default 0.0.0.0:830)
  --host-key=<path>      SSH host key path (default: ephemeral)
  --password=<pw>        Enable SSH password auth
  --adapter=<mock|sysrepo>  Select backend (default: sysrepo)
  --sysrepo-socket=<path>   sysrepo socket (for --adapter=sysrepo)
  --sysrepo-dir=<path>      sysrepo repository path (default: /etc/sysrepo)
  --plugins-dir=<dir>    Directory with libsrplg-*.so
  --plugin=<name>        Allowlist a plugin (repeatable; empty = all)
`
}
