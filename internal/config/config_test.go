package config

import (
	"os"
	"testing"
)

func TestFromFlags(t *testing.T) {
	cfg, err := FromFlags(Default(), []string{"--bind=127.0.0.1:9000", "--password=secret", "--adapter=sysrepo"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.SSH.Bind != "127.0.0.1:9000" {
		t.Errorf("bind: %q", cfg.SSH.Bind)
	}
	if cfg.SSH.Password != "secret" {
		t.Errorf("password: %q", cfg.SSH.Password)
	}
	if cfg.Adapter != "sysrepo" {
		t.Errorf("adapter: %q", cfg.Adapter)
	}
}

func TestFromFlags_SpaceForm(t *testing.T) {
	cfg, err := FromFlags(Default(), []string{"--bind", "0.0.0.0:1234"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.SSH.Bind != "0.0.0.0:1234" {
		t.Errorf("bind: %q", cfg.SSH.Bind)
	}
}

func TestFromFlags_Plugins(t *testing.T) {
	cfg, err := FromFlags(Default(), []string{
		"--plugins-dir=/usr/lib/confd/plugins",
		"--plugin=ietf-system",
		"--plugin=ietf-interfaces",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.Plugins.Dir != "/usr/lib/confd/plugins" {
		t.Errorf("plugins-dir: %q", cfg.Plugins.Dir)
	}
	if len(cfg.Plugins.Names) != 2 || cfg.Plugins.Names[0] != "ietf-system" || cfg.Plugins.Names[1] != "ietf-interfaces" {
		t.Errorf("plugins: %v", cfg.Plugins.Names)
	}
}

func TestLoadConfig_NonExistent(t *testing.T) {
	cfg, err := LoadConfig("/nonexistent/confd.yaml")
	if err != nil {
		t.Fatalf("expected no error for non-existent file, got: %v", err)
	}
	if cfg.SSH.Bind != "0.0.0.0:830" {
		t.Errorf("expected default bind, got %q", cfg.SSH.Bind)
	}
}

func TestLoadConfig_YAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/confd.yaml"
	yaml := `
ssh:
  bind: "127.0.0.1:9999"
  password: "secret"
adapter: mock
plugins:
  dir: "/usr/lib/confd/plugins"
  entries:
    - name: ietf-system
      yang_dir: /usr/lib/confd/yang/ietf-system
      modules:
        - ietf-system@2014-08-06.yang
  names:
    - ietf-system
`
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.SSH.Bind != "127.0.0.1:9999" {
		t.Errorf("bind: %q", cfg.SSH.Bind)
	}
	if cfg.SSH.Password != "secret" {
		t.Errorf("password: %q", cfg.SSH.Password)
	}
	if cfg.Adapter != "mock" {
		t.Errorf("adapter: %q", cfg.Adapter)
	}
	if cfg.Plugins.Dir != "/usr/lib/confd/plugins" {
		t.Errorf("plugins.dir: %q", cfg.Plugins.Dir)
	}
	if len(cfg.Plugins.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(cfg.Plugins.Entries))
	}
	if cfg.Plugins.Entries[0].Name != "ietf-system" {
		t.Errorf("entry name: %q", cfg.Plugins.Entries[0].Name)
	}
	if len(cfg.Plugins.Names) != 1 || cfg.Plugins.Names[0] != "ietf-system" {
		t.Errorf("names: %v", cfg.Plugins.Names)
	}
}

func TestFromFlags_OverridesYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/confd.yaml"
	yaml := `
ssh:
  bind: "127.0.0.1:9999"
adapter: mock
`
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := FromFlags(Default(), []string{"--config=" + path, "--bind=0.0.0.0:8080", "--adapter=sysrepo"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.SSH.Bind != "0.0.0.0:8080" {
		t.Errorf("bind should be overridden by CLI: %q", cfg.SSH.Bind)
	}
	if cfg.Adapter != "sysrepo" {
		t.Errorf("adapter should be overridden by CLI: %q", cfg.Adapter)
	}
}
