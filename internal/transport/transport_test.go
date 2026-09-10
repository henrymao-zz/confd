package transport

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestPipe_RoundTrip(t *testing.T) {
	client, server := Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_ = client.WriteMessage([]byte(`<rpc message-id="1"><get/></rpc>`))
	}()
	got, err := server.ReadMessage()
	if err != nil {
		t.Fatalf("server read: %v", err)
	}
	if !strings.Contains(string(got), "get") {
		t.Errorf("server got: %q", got)
	}
}

func TestSSH_ListenerHandshake(t *testing.T) {
	ln, err := NewSSH(SSHConfig{Bind: "127.0.0.1:0", Password: "s3cret"})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		srv, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer srv.Close()
		// Read one message from the client.
		_, err = srv.ReadMessage()
		done <- err
	}()

	cliCfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("s3cret")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	c, err := ssh.Dial("tcp", ln.Addr().String(), cliCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	ch, _, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	defer ch.Close()

	// Wrap the SSH channel in the framing writer the same way the server does.
	// We just write a base:1.0 framed message directly.
	msg := []byte("<hello/>]]>]]>\n")
	if _, err := ch.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := <-done; err != nil {
		t.Errorf("server read: %v", err)
	}
	_ = bytes.NewReader
}
