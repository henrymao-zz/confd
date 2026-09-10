package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPlugins_All(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"libsrplg-ietf-system.so",
		"libsrplg-ietf-interfaces.so",
		"libsrplg-ietf-routing.so",
	}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.WriteFile(p, []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	specs, err := discoverPlugins(dir, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(specs) != 3 {
		t.Fatalf("expected 3 specs, got %d: %v", len(specs), specs)
	}
	got := map[string]string{}
	for _, s := range specs {
		got[s.Name] = s.Path
	}
	for _, want := range []string{"ietf-system", "ietf-interfaces", "ietf-routing"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing spec %q in %v", want, got)
		}
	}
}

func TestDiscoverPlugins_Allowlist(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"libsrplg-ietf-system.so", "libsrplg-ietf-interfaces.so"} {
		p := filepath.Join(dir, f)
		if err := os.WriteFile(p, []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	specs, err := discoverPlugins(dir, []string{"ietf-interfaces"})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d: %v", len(specs), specs)
	}
	if specs[0].Name != "ietf-interfaces" {
		t.Errorf("name: %q", specs[0].Name)
	}
}

func TestDiscoverPlugins_Empty(t *testing.T) {
	specs, err := discoverPlugins(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected 0 specs, got %d", len(specs))
	}
}
