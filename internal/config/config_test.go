package config

import "testing"

func TestFromFlags(t *testing.T) {
	cfg, err := FromFlags(Default(), []string{"--bind=127.0.0.1:9000", "--password=secret", "--adapter=sysrepo", "--yang-path=/x", "--yang-path=/y"})
	if err != nil {
		t.Fatalf("from flags: %v", err)
	}
	if cfg.SSHBind != "127.0.0.1:9000" {
		t.Errorf("bind: %q", cfg.SSHBind)
	}
	if cfg.SSHPassword != "secret" {
		t.Errorf("password: %q", cfg.SSHPassword)
	}
	if cfg.Adapter != "sysrepo" {
		t.Errorf("adapter: %q", cfg.Adapter)
	}
	if len(cfg.YANGPaths) < 2 || cfg.YANGPaths[len(cfg.YANGPaths)-2] != "/x" || cfg.YANGPaths[len(cfg.YANGPaths)-1] != "/y" {
		t.Errorf("yang paths: %v", cfg.YANGPaths)
	}
}

func TestFromFlags_Unknown(t *testing.T) {
	if _, err := FromFlags(Default(), []string{"--nonsense"}); err == nil {
		t.Error("expected error for unknown flag")
	}
}

func TestFromFlags_SpaceForm(t *testing.T) {
	cfg, err := FromFlags(Default(), []string{"--bind", "0.0.0.0:1234"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.SSHBind != "0.0.0.0:1234" {
		t.Errorf("bind: %q", cfg.SSHBind)
	}
}
