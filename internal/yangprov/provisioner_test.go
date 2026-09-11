package yangprov

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example/confd/internal/schema"
	"github.com/example/confd/internal/sysrepoadapter"
)

func testYangDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "yang"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestProvisioner_Provision(t *testing.T) {
	mock := sysrepoadapter.NewMock(nil)
	conn, err := mock.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	p := New(conn)

	yangDir := testYangDir(t)

	specs := []PluginSpec{
		{
			Name:    "confd-test",
			YangDir: yangDir,
			Modules: []string{"confd-test.yang"},
		},
	}

	// Provision should install the module.
	if err := p.Provision(context.Background(), specs); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// Verify it's installed.
	modules, err := conn.GetModuleInfo(context.Background())
	if err != nil {
		t.Fatalf("get module info: %v", err)
	}
	found := false
	for _, m := range modules {
		if m.Name == "confd-test" {
			found = true
		}
	}
	if !found {
		t.Error("confd-test not installed after provision")
	}

	// Provision again — should be idempotent (no error).
	if err := p.Provision(context.Background(), specs); err != nil {
		t.Fatalf("provision (idempotent): %v", err)
	}
}

func TestProvisioner_LoadCache(t *testing.T) {
	mock := sysrepoadapter.NewMock(nil)
	conn, _ := mock.Connect(context.Background())
	p := New(conn)

	yangDir := testYangDir(t)
	cache := schema.New()
	if err := p.LoadCache(cache, []PluginSpec{{YangDir: yangDir}}); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	mods := cache.Modules()
	if len(mods) == 0 {
		t.Fatal("cache has no modules")
	}
	if mods[0].Name != "confd-test" {
		t.Errorf("expected confd-test, got %s", mods[0].Name)
	}
}

func TestAutoDiscover(t *testing.T) {
	dir := t.TempDir()
	// Create a fake plugin structure.
	pluginDir := filepath.Join(dir, "ietf-system", "yang")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "ietf-system@2024-01-01.yang"), []byte("module ietf-system { }"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "iana-crypt-hash.yang"), []byte("module iana-crypt-hash { }"), 0644); err != nil {
		t.Fatal(err)
	}

	specs := AutoDiscover(dir)
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if specs[0].Name != "ietf-system" {
		t.Errorf("expected ietf-system, got %s", specs[0].Name)
	}
	if len(specs[0].Modules) != 2 {
		t.Fatalf("expected 2 modules, got %d", len(specs[0].Modules))
	}
}

func TestExtractModuleName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yang")
	if err := os.WriteFile(path, []byte("module test-module {\n  namespace \"urn:ietf:params:xml:ns:yang:test\";\n  prefix tm;\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	name := extractModuleName(path)
	if name != "test-module" {
		t.Errorf("expected test-module, got %s", name)
	}
}
