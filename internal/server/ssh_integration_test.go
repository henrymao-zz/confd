package server

import (
	"context"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"

	"nemith.io/netconf"
	"nemith.io/netconf/transport"

	"github.com/example/confd/internal/sysrepoadapter"
	tr "github.com/example/confd/internal/transport"
	"golang.org/x/crypto/ssh"
)

// TestServer_SSHEndToEnd spins up the server over real SSH on a loopback
// port and drives a full <hello> + <get-config> exchange with an
// ssh.Dial-based client. Both sides use nemith's framer.
func TestServer_SSHEndToEnd(t *testing.T) {
	mock := sysrepoadapter.NewMock(nil)
	srv, err := New(context.Background(), Config{
		YANGPaths: []string{yangDir(t)},
		Adapter:   mock,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	mock.SetData(sysrepoadapter.Running, sampleTree())
	mock.SetData(sysrepoadapter.Operational, sampleTree())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := tr.NewSSH(tr.SSHConfig{
		Bind:     "127.0.0.1:0",
		Password: "s3cret",
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	listenErr := make(chan error, 1)
	go func() { listenErr <- srv.ServeSSHListener(ctx, ln) }()

	cliCfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("s3cret")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	}
	conn, err := ssh.Dial("tcp", ln.Addr().String(), cliCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ch, _, err := conn.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	defer ch.Close()

	// Use nemith's framer on the client side too.
	framer := transport.NewFramer(ch, ch)

	// Read server hello.
	hello, err := readFramed[netconf.Hello](framer)
	if err != nil {
		t.Fatalf("read server hello: %v", err)
	}
	if !strings.Contains(strings.Join(hello.Capabilities, ","), netconf.CapNetConf11) {
		t.Fatalf("server hello missing base:1.1: %s", hello.Capabilities)
	}

	// Send client hello.
	if err := writeFramed(framer, &netconf.Hello{
		Capabilities: []string{netconf.CapNetConf10, netconf.CapNetConf11},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	framer.Upgrade()

	// <get-config> from running.
	if err := writeFramedRaw(framer, `<rpc message-id="9001" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-config><source><running/></source></get-config></rpc>`); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
	reply, err := readRawFramed(framer)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(reply, "router-1") {
		t.Errorf("get-config reply missing data: %s", reply)
	}

	// <get> operational.
	if err := writeFramedRaw(framer, `<rpc message-id="9002" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get/></rpc>`); err != nil {
		t.Fatalf("write get: %v", err)
	}
	reply2, err := readRawFramed(framer)
	if err != nil {
		t.Fatalf("read get reply: %v", err)
	}
	if !strings.Contains(reply2, "uptime-seconds") {
		t.Errorf("get reply missing state: %s", reply2)
	}

	// <close-session>.
	_ = writeFramedRaw(framer, `<rpc message-id="9003" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><close-session/></rpc>`)
	_, _ = readRawFramed(framer)
}

func writeFramedRaw(f *transport.Framer, xmlStr string) error {
	w, err := f.MsgWriter()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(xmlStr)); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func writeFramed(f *transport.Framer, v any) error {
	w, err := f.MsgWriter()
	if err != nil {
		return err
	}
	if err := xml.NewEncoder(w).Encode(v); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func readFramed[T any](f *transport.Framer) (T, error) {
	var zero T
	r, err := f.MsgReader()
	if err != nil {
		return zero, err
	}
	defer r.Close()
	var v T
	if err := xml.NewDecoder(r).Decode(&v); err != nil {
		return zero, err
	}
	return v, nil
}

func readRawFramed(f *transport.Framer) (string, error) {
	r, err := f.MsgReader()
	if err != nil {
		return "", err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
