package framing

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWriterReader_Base10RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	msgs := []string{
		`<?xml version="1.0"?><hello/>`,
		`<rpc message-id="1"><get/></rpc>`,
	}
	for _, m := range msgs {
		if err := w.WriteMessage([]byte(m)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	r := NewReader(&buf)
	for i, want := range msgs {
		got, err := r.ReadMessage()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(got) != want {
			t.Fatalf("read %d: got %q want %q", i, got, want)
		}
	}
	if _, err := r.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestWriterReader_Base11RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Upgrade()
	// Force a multi-chunk message by writing a payload larger than 0xffff.
	big := strings.Repeat("X", MaxChunkSize+123)
	msgs := []string{
		`<rpc message-id="7"><get-config><source><running/></source></get-config></rpc>`,
		big,
	}
	for _, m := range msgs {
		if err := w.WriteMessage([]byte(m)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	r := NewReader(&buf)
	r.Upgrade()
	for i, want := range msgs {
		got, err := r.ReadMessage()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(got) != want {
			t.Fatalf("read %d: len got=%d want=%d", i, len(got), len(want))
		}
	}
	if _, err := r.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReader_Base10EOMOnly(t *testing.T) {
	in := "\n]]>]]>\n"
	r := NewReader(strings.NewReader(in))
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty message, got %q", got)
	}
}

func TestParseHexLen(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"0004", 4, true},
		{"ffff", 0xffff, true},
		{"0000", 0, true},
		{"00ff", 0xff, true},
		{"xyz", 0, false},
		{"123", 0, false}, // odd length
		{"", 0, false},
		{"12345", 0, false},
	}
	for _, c := range cases {
		got, ok := parseHexLen(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseHexLen(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
