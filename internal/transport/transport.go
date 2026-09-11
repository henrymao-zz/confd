// Package transport provides the SSH transport layer for NETCONF.
// Framing (RFC 6242) is handled by nemith.io/netconf/transport.Framer
// via the NewNemithTransport adapter.
package transport

import (
	"io"
	"net"

	"nemith.io/netconf/transport"
)

// Transport is a bidirectional SSH channel for NETCONF. The actual
// RFC 6242 framing is handled by nemith's transport.Framer, obtained
// via NewNemithTransport().
type Transport interface {
	// PeerUser returns the authenticated peer username, if any.
	PeerUser() string
	// Close tears down the transport.
	Close() error
	// RawChannel returns the underlying io.Reader + io.Writer (the SSH
	// channel) for wrapping with nemith's transport.Framer.
	RawChannel() (io.Reader, io.Writer)
}

// Listener is a NETCONF transport listener.
type Listener interface {
	Accept() (Transport, error)
	Close() error
	Addr() net.Addr
}

// NemithTransport wraps a Transport's raw SSH channel with nemith's
// transport.Framer. It implements the interface that nettrans.ServerLoop
// expects (MsgReader/MsgWriter/Upgrade/Close).
type NemithTransport struct {
	framer *transport.Framer
	close  func() error
}

// NewNemithTransport wraps a Transport with nemith's framer.
func NewNemithTransport(t Transport) *NemithTransport {
	r, w := t.RawChannel()
	return &NemithTransport{
		framer: transport.NewFramer(r, w),
		close:  t.Close,
	}
}

func (t *NemithTransport) MsgReader() (io.ReadCloser, error) { return t.framer.MsgReader() }
func (t *NemithTransport) MsgWriter() (io.WriteCloser, error) { return t.framer.MsgWriter() }
func (t *NemithTransport) Upgrade()                           { t.framer.Upgrade() }
func (t *NemithTransport) Close() error                        { return t.close() }
