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

	"github.com/example/confd/internal/config"
	"github.com/example/confd/internal/pluginhost"
	"github.com/example/confd/internal/server"
	"github.com/example/confd/internal/sysrepoadapter"
	"github.com/example/confd/internal/transport"
	"github.com/example/confd/internal/yangprov"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, config.Default())
		fmt.Fprintln(os.Stderr, "usage: confd <serve|schema-list> [flags]")
		os.Exit(2)
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
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
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

	// --- plugin specs (from YAML entries + CLI --plugin allowlist) ---
	specs := filterPluginSpecs(cfg.Plugins.Entries, cfg.Plugins.Names)

	// Convert yangprov.PluginSpec to pluginhost.Spec for the plugin host.
	var phSpecs []pluginhost.Spec
	for _, s := range specs {
		phSpecs = append(phSpecs, pluginhost.Spec{Name: s.Name, Path: filepath.Join(cfg.Plugins.Dir, "libsrplg-"+s.Name+".so")})
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
