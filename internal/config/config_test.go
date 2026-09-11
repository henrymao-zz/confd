package config

import "testing"

func TestFromFlags(t *testing.T) {
	cfg, err := FromFlags(Default(), []string{"--bind=127.0.0.1:9000", "--password=secret", "--adapter=sysrepo", "--yang-manifest=/etc/confd/plugins.yaml"})
	if err != nil {
		t.Fatalf("err: %v", err)
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
	if cfg.YangManifest != "/etc/confd/plugins.yaml" {
		t.Errorf("yang-manifest: %q", cfg.YangManifest)
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

func TestFromFlags_Plugins(t *testing.T) {
	cfg, err := FromFlags(Default(), []string{
		"--plugins-dir=/usr/lib/confd/plugins",
		"--plugin=ietf-system",
		"--plugin=ietf-interfaces",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.PluginsDir != "/usr/lib/confd/plugins" {
		t.Errorf("plugins-dir: %q", cfg.PluginsDir)
	}
	if len(cfg.Plugins) != 2 || cfg.Plugins[0] != "ietf-system" || cfg.Plugins[1] != "ietf-interfaces" {
		t.Errorf("plugins: %v", cfg.Plugins)
	}
}

func TestFromFlags_YangManifest(t *testing.T) {
	cfg, err := FromFlags(Default(), []string{"--yang-manifest=/etc/confd/plugins.yaml"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.YangManifest != "/etc/confd/plugins.yaml" {
		t.Errorf("yang-manifest: %q", cfg.YangManifest)
	}
}
