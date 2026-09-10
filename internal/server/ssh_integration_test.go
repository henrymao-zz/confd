package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example/confd/internal/framing"
	"github.com/example/confd/internal/hello"
	"github.com/example/confd/internal/transport"
	"golang.org/x/crypto/ssh"
)

// TestServer_SSHEndToEnd spins up the server over real SSH on a loopback
// port and drives a full <hello> + <get-config> exchange with an
// ssh.Dial-based client.
func TestServer_SSHEndToEnd(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	ln := newSSHListener(t, srv)
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

	r := framing.NewReader(ch)
	w := framing.NewWriter(ch)

	// Read server hello (base:1.0 framing).
	srvHello, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read server hello: %v", err)
	}
	if !strings.Contains(string(srvHello), "base:1.1") {
		t.Fatalf("server hello missing base:1.1: %s", srvHello)
	}
	// Send client hello.
	if err := w.WriteMessage([]byte(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>` + hello.Base11 + `</capability></capabilities></hello>`)); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	r.Upgrade()
	w.Upgrade()

	// <get-config> from running.
	if err := w.WriteMessage([]byte(`<rpc message-id="9001" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-config><source><running/></source></get-config></rpc>`)); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
	reply, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(string(reply), "router-1") {
		t.Errorf("get-config reply missing data: %s", reply)
	}
	if !strings.Contains(string(reply), `message-id="9001"`) {
		t.Errorf("reply missing message-id: %s", reply)
	}

	// <get> operational.
	if err := w.WriteMessage([]byte(`<rpc message-id="9002" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get/></rpc>`)); err != nil {
		t.Fatalf("write get: %v", err)
	}
	reply2, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read get reply: %v", err)
	}
	if !strings.Contains(string(reply2), "uptime-seconds") {
		t.Errorf("get reply missing state: %s", reply2)
	}
}

func newSSHListener(t *testing.T, srv *Server) transport.Listener {
	t.Helper()
	ln, err := transport.NewSSH(transport.SSHConfig{
		Bind:     "127.0.0.1:0",
		Password: "s3cret",
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}
