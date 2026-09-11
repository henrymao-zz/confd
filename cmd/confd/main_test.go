package main

import (
	"path/filepath"
	"testing"

	"github.com/example/confd/internal/yangprov"
)

func TestFilterPluginSpecs_All(t *testing.T) {
	entries := []yangprov.PluginSpec{
		{Name: "ietf-system", YangDir: "/usr/lib/confd/yang/ietf-system"},
		{Name: "ietf-interfaces", YangDir: "/usr/lib/confd/yang/ietf-interfaces"},
		{Name: "ietf-routing", YangDir: "/usr/lib/confd/yang/ietf-routing"},
	}
	specs := filterPluginSpecs(entries, nil)
	if len(specs) != 3 {
		t.Fatalf("expected 3 specs, got %d", len(specs))
	}
}

func TestFilterPluginSpecs_Allowlist(t *testing.T) {
	entries := []yangprov.PluginSpec{
		{Name: "ietf-system", YangDir: "/usr/lib/confd/yang/ietf-system"},
		{Name: "ietf-interfaces", YangDir: "/usr/lib/confd/yang/ietf-interfaces"},
	}
	specs := filterPluginSpecs(entries, []string{"ietf-interfaces"})
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if specs[0].Name != "ietf-interfaces" {
		t.Errorf("name: %q", specs[0].Name)
	}
}

func TestFilterPluginSpecs_Empty(t *testing.T) {
	specs := filterPluginSpecs(nil, nil)
	if len(specs) != 0 {
		t.Errorf("expected 0 specs, got %d", len(specs))
	}
	_ = filepath.Join // suppress unused import
}
