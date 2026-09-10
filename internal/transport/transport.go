// Package transport provides the NETCONF transport layer: a framed
// message stream over either SSH (RFC 6242) or a plain io.ReadWriteCloser
// (used by tests and a debug TCP transport).
package transport

import (
	"io"
	"net"

	"github.com/example/confd/internal/framing"
)

// Transport is a framed, bidirectional NETCONF message stream.
type Transport interface {
	// ReadMessage reads one complete NETCONF message.
	ReadMessage() ([]byte, error)
	// WriteMessage frames and writes one complete NETCONF message.
	WriteMessage(msg []byte) error
	// Framing returns the underlying framing reader/writer so the server
	// can upgrade to base:1.1 after <hello> negotiation.
	Framing() (*framing.Reader, *framing.Writer)
	// PeerUser returns the authenticated peer username, if any.
	PeerUser() string
	// Close tears down the transport.
	Close() error
}

// Pipe builds a pair of in-memory transports backed by a duplex pipe. Used
// for tests: one end is the "client", the other is handed to the server's
// session loop. Both start in base:1.0 framing.
func Pipe() (client Transport, server Transport) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	client = &pipeTransport{r: framing.NewReader(ar), w: framing.NewWriter(aw), rawC: aw, rawR: ar}
	server = &pipeTransport{r: framing.NewReader(br), w: framing.NewWriter(bw), rawC: bw, rawR: br}
	return client, server
}

type pipeTransport struct {
	r    *framing.Reader
	w    *framing.Writer
	rawC io.Closer
	rawR io.Closer
}

func (t *pipeTransport) ReadMessage() ([]byte, error) {
	return t.r.ReadMessage()
}

func (t *pipeTransport) WriteMessage(msg []byte) error {
	return t.w.WriteMessage(msg)
}

func (t *pipeTransport) Framing() (*framing.Reader, *framing.Writer) { return t.r, t.w }

func (t *pipeTransport) PeerUser() string { return "test" }

func (t *pipeTransport) Close() error {
	_ = t.rawC.Close()
	_ = t.rawR.Close()
	return nil
}

// Listener is a NETCONF transport listener.
type Listener interface {
	Accept() (Transport, error)
	Close() error
	Addr() net.Addr
}
