// Package nettrans provides the server-side NETCONF transport and RPC
// dispatch loop built on top of nemith.io/netconf's framing and message
// types. nemith ships only a client-side Session; this package fills the
// server-side gap.
package nettrans

import (
	"io"
	"net"

	"nemith.io/netconf/transport"
	"golang.org/x/crypto/ssh"
)

// SSHServerTransport implements transport.Transport for the server side
// of a NETCONF-over-SSH session. It wraps an ssh.Channel with a Framer.
type SSHServerTransport struct {
	conn   *ssh.ServerConn
	ch     ssh.Channel
	framer *transport.Framer
}

// NewSSHServerTransport wraps an SSH channel + connection with nemith's
// RFC 6242 framer. The caller is responsible for accepting the SSH
// connection and handling the "subsystem" request.
func NewSSHServerTransport(conn *ssh.ServerConn, ch ssh.Channel) *SSHServerTransport {
	return &SSHServerTransport{
		conn:   conn,
		ch:     ch,
		framer: transport.NewFramer(ch, ch),
	}
}

// MsgReader returns a reader for the next framed NETCONF message.
func (t *SSHServerTransport) MsgReader() (io.ReadCloser, error) {
	return t.framer.MsgReader()
}

// MsgWriter returns a writer for a new framed NETCONF message.
func (t *SSHServerTransport) MsgWriter() (io.WriteCloser, error) {
	return t.framer.MsgWriter()
}

// Upgrade switches from base:1.0 EOM framing to base:1.1 chunked framing.
func (t *SSHServerTransport) Upgrade() { t.framer.Upgrade() }

// Close tears down the SSH channel and connection.
func (t *SSHServerTransport) Close() error {
	_ = t.ch.Close()
	return t.conn.Close()
}

// PeerUser returns the authenticated SSH username.
func (t *SSHServerTransport) PeerUser() string { return t.conn.User() }

// PipeTransport creates a pair of in-memory transports for testing,
// connected by net.Pipe (buffered, unlike io.Pipe). Both start in
// base:1.0 framing mode.
type PipeTransport struct {
	framer *transport.Framer
	conn   net.Conn
}

func (t *PipeTransport) MsgReader() (io.ReadCloser, error) { return t.framer.MsgReader() }
func (t *PipeTransport) MsgWriter() (io.WriteCloser, error) { return t.framer.MsgWriter() }
func (t *PipeTransport) Upgrade()                           { t.framer.Upgrade() }
func (t *PipeTransport) Close() error                        { return t.conn.Close() }
func (t *PipeTransport) RawConn() (io.Reader, io.Writer)     { return t.conn, t.conn }

// NewPipe returns a pair of connected PipeTransports (client, server)
// for testing, backed by net.Pipe (synchronous but buffered).
func NewPipe() (*PipeTransport, *PipeTransport) {
	ca, cb := net.Pipe()
	client := &PipeTransport{framer: transport.NewFramer(ca, ca), conn: ca}
	server := &PipeTransport{framer: transport.NewFramer(cb, cb), conn: cb}
	return client, server
}