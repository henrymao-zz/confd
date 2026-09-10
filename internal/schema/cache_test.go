package schema

import (
	"path/filepath"
	"strings"
	"testing"
)

func testYangDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "yang"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCache_LoadConfdTest(t *testing.T) {
	c := New()
	if err := c.LoadDirectory(testYangDir(t)); err != nil {
		t.Fatalf("load: %v", err)
	}
	m := c.Module("confd-test")
	if m == nil {
		t.Fatal("module confd-test not loaded")
	}
	if m.Namespace != "urn:ietf:params:xml:ns:yang:confd-test" {
		t.Errorf("namespace: %q", m.Namespace)
	}
	if m.Revision != "2024-01-01" {
		t.Errorf("revision: %q", m.Revision)
	}
	if m.Prefix != "ct" {
		t.Errorf("prefix: %q", m.Prefix)
	}
	if m.Entry == nil || m.Entry.Name != "confd-test" {
		t.Fatalf("entry: %+v", m.Entry)
	}
	// Find the system container.
	var system *EntryInfo
	for _, ch := range m.Entry.Children {
		if ch.Name == "system" {
			system = ch
		}
	}
	if system == nil {
		t.Fatalf("no system container in children: %+v", m.Entry.Children)
	}
	if !system.IsConfig {
		t.Error("system should be config=true")
	}
	// Find state container which is config false.
	var state *EntryInfo
	for _, ch := range m.Entry.Children {
		if ch.Name == "state" {
			state = ch
		}
	}
	if state == nil {
		t.Fatal("no state container")
	}
	if state.IsConfig {
		t.Error("state should be config=false")
	}
}

func TestCache_Capabilities(t *testing.T) {
	c := New()
	if err := c.LoadDirectory(testYangDir(t)); err != nil {
		t.Fatalf("load: %v", err)
	}
	caps := c.Capabilities()
	found := false
	for _, cap := range caps {
		if strings.HasPrefix(cap, "urn:ietf:params:xml:ns:yang:confd-test") {
			found = true
			if !strings.Contains(cap, "revision=2024-01-01") {
				t.Errorf("cap missing revision: %s", cap)
			}
		}
	}
	if !found {
		t.Errorf("confd-test capability not advertised: %v", caps)
	}
}

func TestCache_SourceText(t *testing.T) {
	c := New()
	if err := c.LoadDirectory(testYangDir(t)); err != nil {
		t.Fatalf("load: %v", err)
	}
	txt, ok := c.SourceText("confd-test")
	if !ok {
		t.Fatal("no source text")
	}
	if !strings.Contains(txt, "module confd-test") {
		t.Errorf("source text missing module decl: %q", txt[:80])
	}
}

func TestCache_ModuleByNamespace(t *testing.T) {
	c := New()
	if err := c.LoadDirectory(testYangDir(t)); err != nil {
		t.Fatalf("load: %v", err)
	}
	m := c.ModuleByNamespace("urn:ietf:params:xml:ns:yang:confd-test")
	if m == nil || m.Name != "confd-test" {
		t.Fatalf("by namespace: %+v", m)
	}
}

func TestCache_LoadEmpty(t *testing.T) {
	c := New()
	if err := c.LoadDirectory(t.TempDir()); err != nil {
		t.Errorf("empty dir: %v", err)
	}
	if len(c.Modules()) != 0 {
		t.Errorf("expected no modules, got %d", len(c.Modules()))
	}
}
