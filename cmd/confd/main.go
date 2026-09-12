// Command confd is a Go-based NETCONF server backed by sysrepo.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/example/confd/internal/cli"
	"github.com/example/confd/internal/config"
	"github.com/example/confd/internal/pluginhost"
	"github.com/example/confd/internal/server"
	"github.com/example/confd/internal/sysrepoadapter"
	"github.com/example/confd/internal/transport"
	"github.com/example/confd/internal/yangprov"
)

func main() {
	if len(os.Args) < 2 {
		// No subcommand → interactive CLI shell
		if err := cli.New().Run(); err != nil {
			fmt.Fprintln(os.Stderr, "confd:", err)
			os.Exit(1)
		}
		return
	}
	switch os.Args[1] {
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "confd:", err)
			os.Exit(1)
		}
	case "schema-list":
		if err := runSchemaList(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "confd:", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		// Try as a CLI command (e.g., "confd connect 127.0.0.1:830")
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `confd - Go-based NETCONF server and CLI

Usage:
  confd                         Interactive NETCONF CLI shell
  confd serve [flags]           Start the NETCONF server
  confd schema-list [flags]     List loaded YANG modules

Run 'confd help' for more information.`)
}

func runServe(args []string) error {
	// Start with defaults, then load YAML config (if --config or
	// /etc/confd/confd.yaml exists), then apply CLI flag overrides.
	cfg, err := config.FromFlags(config.Default(), args)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var adapter sysrepoadapter.Adapter
	switch cfg.Adapter {
	case "sysrepo":
		adapter = sysrepoadapter.NewCGo(cfg.SysrepoSocket)
	case "mock", "":
		adapter = sysrepoadapter.NewMock(nil)
	default:
		return fmt.Errorf("unknown adapter: %s", cfg.Adapter)
	}

	// --- plugin specs (from YAML entries, or auto-discover from --plugins-dir) ---
	var specs []yangprov.PluginSpec
	if len(cfg.Plugins.Entries) > 0 {
		// Use entries from the YAML config.
		specs = filterPluginSpecs(cfg.Plugins.Entries, cfg.Plugins.Names)
	} else if cfg.Plugins.Dir != "" {
		// No YAML entries; auto-discover .so files from --plugins-dir.
		phSpecs, err := discoverPlugins(cfg.Plugins.Dir, cfg.Plugins.Names)
		if err != nil {
			return fmt.Errorf("discover plugins: %w", err)
		}
		for _, ps := range phSpecs {
			specs = append(specs, yangprov.PluginSpec{Name: ps.Name})
		}
	}

	// Convert yangprov.PluginSpec to pluginhost.Spec for the plugin host.
	var phSpecs []pluginhost.Spec
	for _, s := range specs {
		path := filepath.Join(cfg.Plugins.Dir, "libsrplg-"+s.Name+".so")
		if s.YangDir != "" {
			// If from YAML, the .so is in Plugins.Dir.
			path = filepath.Join(cfg.Plugins.Dir, "libsrplg-"+s.Name+".so")
		}
		phSpecs = append(phSpecs, pluginhost.Spec{Name: s.Name, Path: path})
	}

	// --- plugin host (replaces sysrepo-plugind) ---------------------------
	var ph pluginhost.Host
	if len(phSpecs) > 0 {
		ph = pluginhost.New()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, err := server.New(ctx, server.Config{
		Adapter:       adapter,
		PluginHost:    ph,
		PluginSpecs:   phSpecs,
		YangProvSpecs: specs,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	logger.Info("confd starting", "bind", cfg.SSH.Bind, "adapter", cfg.Adapter,
		"modules", len(srv.Cache().Modules()), "plugins", len(specs))
	for _, m := range srv.Cache().Modules() {
		logger.Info("loaded module", "name", m.Name, "ns", m.Namespace, "rev", m.Revision)
	}
	if ph != nil {
		for _, n := range ph.Names() {
			logger.Info("loaded plugin", "name", n)
		}
	}

	return srv.ListenAndServe(ctx, transport.SSHConfig{
		Bind:        cfg.SSH.Bind,
		HostKeyPath: cfg.SSH.HostKey,
		Password:    cfg.SSH.Password,
	})
}

func runSchemaList(args []string) error {
	cfg, err := config.FromFlags(config.Default(), args)
	if err != nil {
		return err
	}
	specs := filterPluginSpecs(cfg.Plugins.Entries, cfg.Plugins.Names)
	srv, err := server.New(context.Background(), server.Config{
		Adapter:       sysrepoadapter.NewMock(nil),
		YangProvSpecs: specs,
	})
	if err != nil {
		return err
	}
	for _, m := range srv.Cache().Modules() {
		fmt.Printf("%-24s %s  rev=%s\n", m.Name, m.Namespace, m.Revision)
	}
	return nil
}

// filterPluginSpecs returns the PluginSpecs from the YAML entries,
// filtered by the CLI --plugin allowlist (if non-empty).
func filterPluginSpecs(entries []yangprov.PluginSpec, names []string) []yangprov.PluginSpec {
	if len(names) == 0 {
		return entries
	}
	allowSet := make(map[string]bool, len(names))
	for _, n := range names {
		allowSet[n] = true
	}
	var specs []yangprov.PluginSpec
	for _, e := range entries {
		if allowSet[e.Name] {
			specs = append(specs, e)
		}
	}
	return specs
}

// discoverPlugins scans dir for libsrplg-<name>.so files and returns
// Specs. If allow is non-empty, only names in the allowlist are included.
func discoverPlugins(dir string, allow []string) ([]pluginhost.Spec, error) {
	allowSet := make(map[string]bool, len(allow))
	for _, n := range allow {
		allowSet[n] = true
	}
	matches, err := filepath.Glob(filepath.Join(dir, "libsrplg-*.so"))
	if err != nil {
		return nil, err
	}
	var specs []pluginhost.Spec
	for _, m := range matches {
		base := filepath.Base(m)
		name := base[len("libsrplg-"):]
		name = name[:len(name)-len(".so")]
		if len(allow) > 0 && !allowSet[name] {
			continue
		}
		specs = append(specs, pluginhost.Spec{Name: name, Path: m})
	}
	return specs, nil
}
