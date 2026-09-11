package cli

import "testing"

func TestParseCommand(t *testing.T) {
	cmd, args := parseCommand("get-config --source running --filter /system")
	if cmd != "get-config" {
		t.Errorf("cmd: %q", cmd)
	}
	if len(args) != 4 {
		t.Fatalf("args: %v", args)
	}
}

func TestParseCommand_Empty(t *testing.T) {
	cmd, args := parseCommand("")
	if cmd != "" {
		t.Errorf("expected empty cmd, got %q", cmd)
	}
	if args != nil {
		t.Errorf("expected nil args")
	}
}

func TestFlagValue(t *testing.T) {
	args := []string{"--source=running", "--filter", "/system"}
	if v := flagValue(args, "source", "startup"); v != "running" {
		t.Errorf("source: %q", v)
	}
	if v := flagValue(args, "filter", ""); v != "/system" {
		t.Errorf("filter: %q", v)
	}
	if v := flagValue(args, "missing", "def"); v != "def" {
		t.Errorf("missing: %q", v)
	}
}
