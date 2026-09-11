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

	// --- plugin host (replaces sysrepo-plugind) ---------------------------
	var ph pluginhost.Host
	var specs []pluginhost.Spec
	if cfg.PluginsDir != "" {
		specs, err = discoverPlugins(cfg.PluginsDir, cfg.Plugins)
		if err != nil {
			return fmt.Errorf("discover plugins: %w", err)
		}
	}
	if len(specs) > 0 {
		ph = pluginhost.New()
	} else {
		ph = nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, err := server.New(ctx, server.Config{
		YANGPaths:    cfg.YANGPaths,
		Adapter:      adapter,
		PluginHost:   ph,
		PluginSpecs:   specs,
		YangManifest: cfg.YangManifest,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	logger.Info("confd starting", "bind", cfg.SSHBind, "adapter", cfg.Adapter,
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
		Bind:        cfg.SSHBind,
		HostKeyPath: cfg.SSHHostKey,
		Password:    cfg.SSHPassword,
	})
}

func runSchemaList(args []string) error {
	cfg, err := config.FromFlags(config.Default(), args)
	if err != nil {
		return err
	}
	srv, err := server.New(context.Background(), server.Config{
		YANGPaths: cfg.YANGPaths,
		Adapter:   sysrepoadapter.NewMock(nil),
	})
	if err != nil {
		return err
	}
	for _, m := range srv.Cache().Modules() {
		fmt.Printf("%-24s %s  rev=%s\n", m.Name, m.Namespace, m.Revision)
	}
	return nil
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
		// strip "libsrplg-" prefix and ".so" suffix
		name := base[len("libsrplg-"):]
		name = name[:len(name)-len(".so")]
		if len(allow) > 0 && !allowSet[name] {
			continue
		}
		specs = append(specs, pluginhost.Spec{Name: name, Path: m})
	}
	return specs, nil
}
