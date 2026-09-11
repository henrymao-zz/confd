// Package server wires together the transport, NETCONF protocol loop,
// schema cache, sysrepo adapter, and plugin host into a runnable server.
// It uses nemith.io/netconf for framing, hello, and message types via
// the internal/nettrans adapter.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/example/confd/internal/data"
	"github.com/example/confd/internal/operations"
	"github.com/example/confd/internal/pluginhost"
	"github.com/example/confd/internal/schema"
	"github.com/example/confd/internal/sysrepoadapter"
	"github.com/example/confd/internal/transport"

	"nemith.io/netconf"
)

// Config configures a Server.
type Config struct {
	// YANGPaths are directories to load YANG modules from for the schema
	// cache.
	YANGPaths []string
	// Adapter is the sysrepo (or mock) backend. If nil, a Mock adapter
	// is used.
	Adapter sysrepoadapter.Adapter
	// Modules is the list of module infos to advertise when using the mock
	// adapter. Ignored when Adapter is non-nil.
	Modules []sysrepoadapter.ModuleInfo
	// PluginHost is the plugin lifecycle manager (replaces sysrepo-plugind).
	// If nil, no plugins are loaded.
	PluginHost pluginhost.Host
	// PluginSpecs is the list of plugins to load via PluginHost on startup.
	PluginSpecs []pluginhost.Spec
}

// Server is a runnable NETCONF server.
type Server struct {
	cfg        Config
	cache      *schema.Cache
	adapter    sysrepoadapter.Adapter
	conn       sysrepoadapter.Conn
	encoder    *data.Encoder
	reg        *operations.SessionRegistry
	pluginHost pluginhost.Host
}

// New builds a Server from cfg.
func New(ctx context.Context, cfg Config) (*Server, error) {
	cache := schema.New()
	for _, dir := range cfg.YANGPaths {
		if err := cache.LoadDirectory(dir); err != nil {
			return nil, fmt.Errorf("server: load schema %s: %w", dir, err)
		}
	}
	adapter := cfg.Adapter
	if adapter == nil {
		adapter = sysrepoadapter.NewMock(cfg.Modules)
	}
	conn, err := adapter.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("server: connect adapter: %w", err)
	}

	// --- plugin host (replaces sysrepo-plugind) ---------------------------
	ph := cfg.PluginHost
	if ph != nil && len(cfg.PluginSpecs) > 0 {
		if err := ph.Start(conn, cfg.PluginSpecs); err != nil {
			slog.Warn("server: plugin host start failed", "error", err)
		} else {
			slog.Info("server: plugins loaded", "names", ph.Names())
		}
	}

	reg := operations.NewSessionRegistry()
	encoder := data.New(cache)
	return &Server{
		cfg:        cfg,
		cache:      cache,
		adapter:    adapter,
		conn:       conn,
		encoder:    encoder,
		reg:        reg,
		pluginHost: ph,
	}, nil
}

// Cache returns the server's schema cache (for inspection / tests).
func (s *Server) Cache() *schema.Cache { return s.cache }

// Close releases server-wide resources. Plugins are stopped in
// reverse load order before the datastore connection is closed.
func (s *Server) Close() error {
	if s.pluginHost != nil {
		_ = s.pluginHost.Stop()
	}
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

// buildCapabilities returns the capability URIs for the <hello> message.
func (s *Server) buildCapabilities() []string {
	caps := []string{netconf.CapNetConf10, netconf.CapNetConf11}
	for _, m := range s.cache.Modules() {
		uri := m.Namespace
		if m.Revision != "" {
			uri += "?revision=" + m.Revision
		}
		caps = append(caps, uri)
	}
	return caps
}

// buildHandlers returns the operation handler map for transport.ServerLoop.
func (s *Server) buildHandlers(sessionID uint64, peerUser string) map[string]transport.Handler {
	deps := operations.Deps{
		Cache:    s.cache,
		Conn:     s.conn,
		Encoder:  s.encoder,
		Sessions: s.reg,
	}
	return operations.BuildHandlers(deps, sessionID, peerUser)
}

// ServeTransport runs the NETCONF protocol over a single session.
// It blocks until the session ends or the transport closes.
// sess must implement MsgReader/MsgWriter/Upgrade/Close/PeerUser
// (i.e. *transport.Session or *transport.PipeTransport).
func (s *Server) ServeTransport(ctx context.Context, sess transport.SessionInterface) error {
	defer sess.Close()
	sessionID := s.reg.Alloc()
	state := &operations.SessionState{
		ID:   sessionID,
		User: sess.PeerUser(),
	}
	s.reg.Register(state)
	defer s.reg.Forget(sessionID)

	handlers := s.buildHandlers(sessionID, state.User)
	caps := s.buildCapabilities()

	return transport.ServerLoop(sess, handlers, caps, sessionID, state.User)
}

// ListenAndServe starts an SSH listener and serves sessions until ctx is
// cancelled.
func (s *Server) ListenAndServe(ctx context.Context, sshCfg transport.SSHConfig) error {
	ln, err := transport.NewSSH(sshCfg)
	if err != nil {
		return err
	}
	defer ln.Close()
	return s.ServeSSHListener(ctx, ln)
}

// ServeSSHListener serves sessions on an existing SSH listener until ctx is
// cancelled or the listener is closed.
func (s *Server) ServeSSHListener(ctx context.Context, ln transport.Listener) error {
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		close(done)
	}()
	for {
		t, err := ln.Accept()
		if err != nil {
			select {
			case <-done:
				wg.Wait()
				return nil
			default:
				return err
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.ServeTransport(ctx, t)
		}()
	}
}