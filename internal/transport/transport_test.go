package transport

import (
	"io"
	"testing"

	"nemith.io/netconf/transport"

	"golang.org/x/crypto/ssh"
)

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
		// Wrap the server's raw channel with nemith's framer.
		nt := NewNemithTransport(srv)
		r, err := nt.MsgReader()
		if err != nil {
			done <- err
			return
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		done <- err
		_ = data
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

	// Write a base:1.0 framed message using nemith's framer on the client side.
	framer := transport.NewFramer(ch, ch)
	w, err := framer.MsgWriter()
	if err != nil {
		t.Fatalf("msg writer: %v", err)
	}
	if _, err := w.Write([]byte("<hello/>")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	if err := <-done; err != nil {
		t.Errorf("server read: %v", err)
	}
}
