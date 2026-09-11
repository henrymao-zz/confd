// Package yangprov implements YANG module provisioning for confd.
// It ensures that all YANG modules required by the enabled plugins are
// installed in sysrepo before the plugins start.
package yangprov

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/confd/internal/schema"
	"github.com/example/confd/internal/sysrepoadapter"
)

// PluginSpec describes one plugin's YANG modules and features.
type PluginSpec struct {
	Name       string            `yaml:"name"`
	YangDir    string            `yaml:"yang_dir"`
	Modules    []string          `yaml:"modules"`
	Features   map[string][]string `yaml:"features"`
	ImportDirs []string          `yaml:"import_search_dirs"`
}

// Provisioner checks that required YANG modules are installed in sysrepo
// and installs any that are missing. It also loads the same YANG directories
// into the goyang cache so that capabilities and <get-schema> match.
type Provisioner struct {
	conn sysrepoadapter.Conn
}

// New returns a Provisioner backed by the given sysrepo connection.
func New(conn sysrepoadapter.Conn) *Provisioner {
	return &Provisioner{conn: conn}
}

// Provision checks each plugin's YANG modules against sysrepo's installed
// modules and installs any that are missing. Features are enabled for
// already-installed modules.
func (p *Provisioner) Provision(ctx context.Context, specs []PluginSpec) error {
	installed, err := p.conn.GetModuleInfo(ctx)
	if err != nil {
		return fmt.Errorf("yangprov: get module info: %w", err)
	}
	installedNames := make(map[string]bool, len(installed))
	for _, m := range installed {
		installedNames[m.Name] = true
	}

	for _, spec := range specs {
		searchDirs := strings.Join(spec.ImportDirs, ":")
		for _, moduleFile := range spec.Modules {
			path := filepath.Join(spec.YangDir, moduleFile)
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("yangprov: %s: %w", path, err)
			}
			moduleName := extractModuleName(path)
			if installedNames[moduleName] {
				// Module already installed; enable features.
				for mod, features := range spec.Features {
					if mod != moduleName {
						continue
					}
					for _, f := range features {
						if err := p.conn.SetModuleFeature(ctx, mod, f, true); err != nil {
							return fmt.Errorf("yangprov: set feature %s on %s: %w", f, mod, err)
						}
					}
				}
				continue
			}
			// Module not installed; install it.
			var features []string
			if spec.Features != nil {
				features = spec.Features[moduleName]
			}
			if err := p.conn.InstallModule(ctx, path, searchDirs, features); err != nil {
				return fmt.Errorf("yangprov: install %s: %w", path, err)
			}
			installedNames[moduleName] = true
		}
	}
	return nil
}

// LoadCache loads the YANG directories from the specs into the goyang
// cache so that <hello> capabilities and <get-schema> match what's
// in sysrepo.
func (p *Provisioner) LoadCache(cache *schema.Cache, specs []PluginSpec) error {
	for _, spec := range specs {
		if spec.YangDir != "" {
			if err := cache.LoadDirectory(spec.YangDir); err != nil {
				return fmt.Errorf("yangprov: load cache %s: %w", spec.YangDir, err)
			}
		}
	}
	return nil
}

// AutoDiscover walks a plugins directory and returns PluginSpecs for
// each subdirectory that contains a yang/ folder. Features are not
// discovered (they require per-plugin knowledge that only the manifest
// can provide).
func AutoDiscover(pluginsDir string) []PluginSpec {
	yangDirs, err := filepath.Glob(filepath.Join(pluginsDir, "*/yang"))
	if err != nil {
		return nil
	}
	var specs []PluginSpec
	for _, yangDir := range yangDirs {
		pluginName := filepath.Base(filepath.Dir(yangDir))
		yangFiles, _ := filepath.Glob(filepath.Join(yangDir, "*.yang"))
		var modules []string
		for _, f := range yangFiles {
			modules = append(modules, filepath.Base(f))
		}
		if len(modules) == 0 {
			continue
		}
		specs = append(specs, PluginSpec{
			Name:    pluginName,
			YangDir: yangDir,
			Modules: modules,
		})
	}
	return specs
}

// extractModuleName reads a .yang file and extracts the module name.
func extractModuleName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			name := strings.TrimPrefix(line, "module ")
			name = strings.TrimSuffix(name, " {")
			return name
		}
	}
	return ""
}
