// Package yangprov implements YANG module provisioning for confd.
// It ensures that all YANG modules required by the enabled plugins are
// installed in sysrepo before the plugins start.
package yangprov

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/confd/internal/schema"
	"github.com/example/confd/internal/sysrepoadapter"
	"github.com/openconfig/goyang/pkg/yang"
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
// already-installed modules. Failed installs are retried up to 3 times
// to handle YANG import dependencies (modules that depend on other
// modules being installed first).
func (p *Provisioner) Provision(ctx context.Context, specs []PluginSpec) error {
	// GetModuleInfo may crash on a fresh sysrepo instance (segfault
	// in sr_get_module_info before internal modules are loaded).
	// Recover from panics and treat as "no modules installed yet".
	var installed []sysrepoadapter.ModuleInfo
	func() {
		defer func() { _ = recover() }()
		installed, _ = p.conn.GetModuleInfo(ctx)
	}()
	installedNames := make(map[string]bool, len(installed))
	for _, m := range installed {
		installedNames[m.Name] = true
	}

	// Collect all modules to install across all specs, with their
	// search dirs and features.
	type pending struct {
		path       string
		searchDirs string
		features   []string
		moduleName string
	}
	var queue []pending
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
							slog.Warn("yangprov: set feature failed (non-fatal)",
								"module", mod, "feature", f, "error", err)
						}
					}
				}
				continue
			}
			var features []string
			if spec.Features != nil {
				features = spec.Features[moduleName]
			}
			queue = append(queue, pending{
				path:       path,
				searchDirs: searchDirs,
				features:   features,
				moduleName: moduleName,
			})
		}
	}

	// Install modules with retries for dependency ordering.
	// Each pass installs what it can; failed modules are retried
	// in the next pass (their dependencies may have been installed
	// in the previous pass).
	const maxPasses = 5
	for pass := 0; pass < maxPasses; pass++ {
		if len(queue) == 0 {
			break
		}
		var failed []pending
		for _, m := range queue {
			if err := p.conn.InstallModule(ctx, m.path, m.searchDirs, m.features); err != nil {
				failed = append(failed, m)
			} else {
				installedNames[m.moduleName] = true
				slog.Info("yangprov: installed module", "name", m.moduleName,
					"pass", pass+1)
			}
		}
		queue = failed
	}

	// Enable features for newly installed modules
	for _, spec := range specs {
		for mod, features := range spec.Features {
			if installedNames[mod] {
				for _, f := range features {
					if err := p.conn.SetModuleFeature(ctx, mod, f, true); err != nil {
						slog.Warn("yangprov: enable feature failed (non-fatal)",
							"module", mod, "feature", f, "error", err)
					}
				}
			}
		}
	}

	if len(queue) > 0 {
		var names []string
		for _, m := range queue {
			names = append(names, m.moduleName)
		}
		return fmt.Errorf("yangprov: failed to install %d modules: %s",
			len(queue), strings.Join(names, ", "))
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

// AutoDiscover walks a YANG directory and returns PluginSpecs for
// each subdirectory that contains .yang files. Features are discovered
// by parsing the YANG files with goyang — every `feature` declaration
// found is enabled by default.
//
// Supported layouts:
//   - <dir>/<plugin>/yang/*.yang   (plugin subdirs with yang/ folder)
//   - <dir>/<plugin>/*.yang        (plugin subdirs with .yang files directly)
//   - <dir>/yang/*.yang            (flat yang/ folder)
//   - <dir>/*.yang                 (flat directory)
func AutoDiscover(pluginsDir string) []PluginSpec {
	var yangDirs []string

	// Layout 1: <dir>/<plugin>/yang/*.yang
	yangDirs, _ = filepath.Glob(filepath.Join(pluginsDir, "*/yang"))

	// Layout 2: <dir>/<plugin>/*.yang (no yang/ subfolder)
	subDirs, _ := filepath.Glob(filepath.Join(pluginsDir, "*"))
	for _, sub := range subDirs {
		fi, err := os.Stat(sub)
		if err != nil || !fi.IsDir() {
			continue
		}
		yangFiles, _ := filepath.Glob(filepath.Join(sub, "*.yang"))
		if len(yangFiles) > 0 {
			// Check it's not already added via layout 1
			already := false
			for _, yd := range yangDirs {
				if yd == sub {
					already = true
					break
				}
			}
			if !already {
				yangDirs = append(yangDirs, sub)
			}
		}
	}

	// Layout 3: <dir>/yang/*.yang
	if len(yangDirs) == 0 {
		yangDir := filepath.Join(pluginsDir, "yang")
		if fi, e := os.Stat(yangDir); e == nil && fi.IsDir() {
			yangDirs = []string{yangDir}
		}
	}

	// Layout 4: <dir>/*.yang
	if len(yangDirs) == 0 {
		yangFiles, _ := filepath.Glob(filepath.Join(pluginsDir, "*.yang"))
		if len(yangFiles) > 0 {
			yangDirs = []string{pluginsDir}
		}
	}

	var specs []PluginSpec
	for _, yangDir := range yangDirs {
		pluginName := filepath.Base(yangDir)
		// For layout 1 (<dir>/<plugin>/yang), use parent dir name
		if filepath.Base(yangDir) == "yang" {
			pluginName = filepath.Base(filepath.Dir(yangDir))
		}
		yangFiles, _ := filepath.Glob(filepath.Join(yangDir, "*.yang"))
		var modules []string
		for _, f := range yangFiles {
			modules = append(modules, filepath.Base(f))
		}
		if len(modules) == 0 {
			continue
		}
		spec := PluginSpec{
			Name:    pluginName,
			YangDir: yangDir,
			Modules: modules,
		}
		// Parse YANG files to discover features
		features := discoverFeatures(yangDir, yangFiles)
		if len(features) > 0 {
			spec.Features = features
		}
		specs = append(specs, spec)
	}
	return specs
}

// discoverFeatures parses YANG files with goyang and returns a map
// of module name → feature names declared in the module.
func discoverFeatures(yangDir string, yangFiles []string) map[string][]string {
	ms := yang.NewModules()
	for _, f := range yangFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		abs, _ := filepath.Abs(f)
		_ = ms.Parse(string(data), abs)
	}
	_ = ms.Process()

	result := map[string][]string{}
	for _, mod := range ms.Modules {
		if mod == nil || mod.Kind() != "module" {
			continue
		}
		for _, feat := range mod.Feature {
			result[mod.Name] = append(result[mod.Name], feat.Name)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
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
