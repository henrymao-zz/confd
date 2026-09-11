package pluginhost

import (
	"testing"

	"github.com/example/confd/internal/sysrepoadapter"
)

func TestMockHost_StartStop(t *testing.T) {
	h := NewMockHost()
	specs := []Spec{
		{Name: "ietf-system", Path: "/usr/lib/confd/plugins/libsrplg-ietf-system.so"},
		{Name: "ietf-interfaces", Path: "/usr/lib/confd/plugins/libsrplg-ietf-interfaces.so"},
		{Name: "fail-broken", Path: "/usr/lib/confd/plugins/libsrplg-fail-broken.so"},
	}
	conn, _ := sysrepoadapter.NewMock(nil).Connect(testCtx())
	if err := h.Start(conn, specs); err != nil {
		t.Fatalf("start: %v", err)
	}
	names := h.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 loaded plugins, got %d: %v", len(names), names)
	}
	if names[0] != "ietf-system" || names[1] != "ietf-interfaces" {
		t.Errorf("names: %v", names)
	}
	if err := h.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(h.Names()) != 0 {
		t.Errorf("after stop, expected 0 names, got %v", h.Names())
	}
}

func TestMockHost_FailPrefix(t *testing.T) {
	h := NewMockHost()
	specs := []Spec{
		{Name: "fail-broken", Path: "/x/libsrplg-fail-broken.so"},
	}
	conn, _ := sysrepoadapter.NewMock(nil).Connect(testCtx())
	if err := h.Start(conn, specs); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(h.Names()) != 0 {
		t.Errorf("expected 0 loaded (fail- prefix), got %v", h.Names())
	}
}

func TestNoopHost(t *testing.T) {
	h := NoopHost{}
	if err := h.Start(nil, nil); err != ErrNotAvailable {
		t.Errorf("expected ErrNotAvailable, got %v", err)
	}
	if err := h.Stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
	if len(h.Names()) != 0 {
		t.Errorf("expected 0 names, got %v", h.Names())
	}
}

func TestNew_DefaultBuild(t *testing.T) {
	h := New()
	// With the `sysrepo` build tag, New() returns CGoHost; without it,
	// NoopHost. Both are valid — just verify it's not nil and is a Host.
	if h == nil {
		t.Fatal("New() returned nil")
	}
	_ = h.Names() // must not panic
}
