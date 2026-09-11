package server

import (
	"context"
	"testing"

	"github.com/example/confd/internal/pluginhost"
	"github.com/example/confd/internal/sysrepoadapter"
)

func TestServer_PluginHost_MockIntegration(t *testing.T) {
	mock := sysrepoadapter.NewMock(nil)
	ph := pluginhost.NewMockHost()
	specs := []pluginhost.Spec{
		{Name: "ietf-system", Path: "/usr/lib/confd/plugins/libsrplg-ietf-system.so"},
		{Name: "ietf-interfaces", Path: "/usr/lib/confd/plugins/libsrplg-ietf-interfaces.so"},
	}

	srv, err := New(context.Background(), Config{
		YangProvSpecs: testYangSpecs(),
		Adapter:     mock,
		PluginHost:  ph,
		PluginSpecs: specs,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	names := ph.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 plugins loaded, got %d: %v", len(names), names)
	}
	if names[0] != "ietf-system" || names[1] != "ietf-interfaces" {
		t.Errorf("plugin names: %v", names)
	}

	// Close should stop the plugin host.
	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(ph.Names()) != 0 {
		t.Errorf("after close, expected 0 plugins, got %v", ph.Names())
	}
}

func TestServer_PluginHost_NoSpecs(t *testing.T) {
	mock := sysrepoadapter.NewMock(nil)
	ph := pluginhost.NewMockHost()
	srv, err := New(context.Background(), Config{
		YangProvSpecs: testYangSpecs(),
		Adapter:    mock,
		PluginHost: ph,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer srv.Close()
	if len(ph.Names()) != 0 {
		t.Errorf("expected 0 plugins, got %v", ph.Names())
	}
}

func TestServer_PluginHost_Nil(t *testing.T) {
	mock := sysrepoadapter.NewMock(nil)
	srv, err := New(context.Background(), Config{
		YangProvSpecs: testYangSpecs(),
		Adapter:    mock,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer srv.Close()
	// No plugin host → no crash, normal operation.
}
