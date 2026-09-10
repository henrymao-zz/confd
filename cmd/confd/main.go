// Command confd is a Go-based NETCONF server backed by sysrepo.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/example/confd/internal/config"
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, err := server.New(ctx, server.Config{
		YANGPaths: cfg.YANGPaths,
		Adapter:   adapter,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	logger.Info("confd starting", "bind", cfg.SSHBind, "adapter", cfg.Adapter, "modules", len(srv.Cache().Modules()))
	for _, m := range srv.Cache().Modules() {
		logger.Info("loaded module", "name", m.Name, "ns", m.Namespace, "rev", m.Revision)
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
