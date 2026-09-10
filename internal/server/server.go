// Package server wires together the transport, framing, hello negotiation,
// rpc dispatcher, schema cache, and sysrepo adapter into a runnable
// NETCONF server. It exposes Server for the cmd/confd entrypoint and for
// end-to-end tests.
package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/example/confd/internal/data"
	"github.com/example/confd/internal/hello"
	"github.com/example/confd/internal/operations"
	"github.com/example/confd/internal/pluginhost"
	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/schema"
	"github.com/example/confd/internal/sysrepoadapter"
	"github.com/example/confd/internal/transport"
)

// Config configures a Server.
type Config struct {
	// YANGPaths are directories to load YANG modules from for the schema
	// cache.
	YANGPaths []string
	// Adapter is the sysrepo (or mock) backend. If nil, a Mock adapter
	// seeded from the schema cache is used.
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
	dispatch   *rpc.Dispatcher
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
	dispatch := rpc.NewDispatcher()
	deps := operations.Deps{
		Cache:    cache,
		Conn:     conn,
		Encoder:  encoder,
		Sessions: reg,
	}
	operations.Register(dispatch, deps)
	return &Server{
		cfg:        cfg,
		cache:      cache,
		adapter:    adapter,
		conn:       conn,
		encoder:    encoder,
		dispatch:   dispatch,
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

// ServeTransport runs the NETCONF protocol over a single Transport (one
// session). It blocks until the session ends or the transport closes.
func (s *Server) ServeTransport(ctx context.Context, t transport.Transport) error {
	defer t.Close()
	sessionID := s.reg.Alloc()
	state := &operations.SessionState{
		ID:      sessionID,
		User:    t.PeerUser(),
	}
	s.reg.Register(state)
	defer s.reg.Forget(sessionID)

	r, w := t.Framing()

	// --- <hello> phase ---------------------------------------------------
	caps := s.buildCapabilities()
	helloMsg := hello.Build(sessionID, caps)
	if err := w.WriteMessage(helloMsg); err != nil {
		return fmt.Errorf("server: send hello: %w", err)
	}
	peerPayload, err := r.ReadMessage()
	if err != nil {
		return fmt.Errorf("server: read peer hello: %w", err)
	}
	peerHello, err := hello.Parse(peerPayload)
	if err != nil {
		return fmt.Errorf("server: parse peer hello: %w", err)
	}
	hello.Negotiate(peerHello, r, w)

	// --- <rpc> phase ----------------------------------------------------
	rctx := rpc.Context{
		SessionID:  sessionID,
		PeerUser:   state.User,
		Dispatcher: s.dispatch,
	}
	for {
		payload, err := r.ReadMessage()
		if err != nil {
			return nil // peer closed / EOF -> session ends cleanly
		}
		out := s.dispatch.Handle(rctx, payload)
		if err := w.WriteMessage(out); err != nil {
			return fmt.Errorf("server: write reply: %w", err)
		}
		// Detect <close-session> by scanning the payload for the op. We
		// rely on the handler returning the close-session sentinel; but
		// since dispatch returns the encoded reply rather than the error,
		// we detect close-session by name here.
		if isCloseSession(payload) {
			return nil
		}
	}
}

func (s *Server) buildCapabilities() []hello.Capability {
	caps := []hello.Capability{{URI: hello.Base11}, {URI: hello.Base10}}
	for _, m := range s.cache.Modules() {
		caps = append(caps, hello.Capability{
			URI:      m.Namespace,
			Revision: m.Revision,
		})
	}
	return caps
}

func isCloseSession(payload []byte) bool {
	return bytes.Contains(payload, []byte("<close-session"))
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
