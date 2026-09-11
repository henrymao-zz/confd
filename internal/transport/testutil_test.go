package transport

import (
	"io"
	"net"

	nemithtransport "nemith.io/netconf/transport"
)

// PipeTransport is an in-memory transport for tests, backed by net.Pipe
// with nemith's Framer on both ends. It satisfies SessionInterface.
type PipeTransport struct {
	framer *nemithtransport.Framer
	conn   net.Conn
}

func (t *PipeTransport) MsgReader() (io.ReadCloser, error) { return t.framer.MsgReader() }
func (t *PipeTransport) MsgWriter() (io.WriteCloser, error) { return t.framer.MsgWriter() }
func (t *PipeTransport) Upgrade()                           { t.framer.Upgrade() }
func (t *PipeTransport) Close() error                        { return t.conn.Close() }
func (t *PipeTransport) PeerUser() string                   { return "test" }

// NewPipe returns a pair of connected PipeTransports (client, server)
// for testing, backed by net.Pipe.
func NewPipe() (*PipeTransport, *PipeTransport) {
	ca, cb := net.Pipe()
	client := &PipeTransport{framer: nemithtransport.NewFramer(ca, ca), conn: ca}
	server := &PipeTransport{framer: nemithtransport.NewFramer(cb, cb), conn: cb}
	return client, server
}
