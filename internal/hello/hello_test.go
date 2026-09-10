package hello

import (
	"strings"
	"testing"

	"github.com/example/confd/internal/framing"
)

func TestBuildAndParse(t *testing.T) {
	caps := []Capability{
		{URI: Base11},
		{URI: "urn:ietf:params:xml:ns:yang:ietf-example", Revision: "2024-01-01", Features: []string{"foo"}},
	}
	out := Build(1, caps)
	if !strings.Contains(string(out), `session-id>1<`) {
		t.Errorf("missing session-id: %s", out)
	}
	if !strings.Contains(string(out), Base11) {
		t.Errorf("missing base:1.1: %s", out)
	}
	if !strings.Contains(string(out), "revision=2024-01-01") {
		t.Errorf("missing revision param: %s", out)
	}
	h, err := Parse(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !h.SupportsBase11() {
		t.Error("expected base:1.1 support")
	}
}

func TestNegotiate_Upgrade(t *testing.T) {
	h := &Hello{Capabilities: []string{Base11}}
	var rw struct{ r *framing.Reader; w *framing.Writer }
	rw.r = framing.NewReader(nil)
	rw.w = framing.NewWriter(nil)
	if !Negotiate(h, rw.r, rw.w) {
		t.Fatal("expected upgrade")
	}
	if rw.r.Mode() != framing.ModeBase11 || rw.w.Mode() != framing.ModeBase11 {
		t.Fatal("framing not upgraded")
	}
}

func TestNegotiate_NoBase11(t *testing.T) {
	h := &Hello{Capabilities: []string{Base10}}
	r := framing.NewReader(nil)
	w := framing.NewWriter(nil)
	if Negotiate(h, r, w) {
		t.Fatal("should not upgrade when peer lacks base:1.1")
	}
}
