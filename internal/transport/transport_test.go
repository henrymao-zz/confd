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
		sess, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer sess.Close()
		r, err := sess.MsgReader()
		if err != nil {
			done <- err
			return
		}
		defer r.Close()
		_, err = io.ReadAll(r)
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
