package transport

import (
	"encoding/xml"
	"io"
	"net"
	"strings"
	"testing"

	"nemith.io/netconf"
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

func TestServerLoop_BasicExchange(t *testing.T) {
	client, server := NewPipe()

	caps := []string{netconf.CapNetConf10, netconf.CapNetConf11}
	handlers := map[string]Handler{
		"get-config": func(msgID string, inner []byte) (any, error) {
			return []byte("<data>router-1</data>"), nil
		},
		"close-session": func(msgID string, inner []byte) (any, error) {
			return []byte("<ok/>"), nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- ServerLoop(server, handlers, caps, 1, "test")
	}()

	hello, err := readMsg[netconf.Hello](client)
	if err != nil {
		t.Fatalf("read server hello: %v", err)
	}
	if hello.SessionID != 1 {
		t.Errorf("session-id: got %d want 1", hello.SessionID)
	}
	if !strings.Contains(strings.Join(hello.Capabilities, ","), netconf.CapNetConf11) {
		t.Error("server hello missing base:1.1")
	}

	if err := writeMsg(client, &netconf.Hello{
		Capabilities: []string{netconf.CapNetConf10, netconf.CapNetConf11},
	}); err != nil {
		t.Fatalf("send client hello: %v", err)
	}
	client.Upgrade()

	if err := writeMsg(client, &struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`
		MessageID string   `xml:"message-id,attr"`
		Op        string   `xml:"get-config"`
	}{MessageID: "1"}); err != nil {
		t.Fatalf("send get-config: %v", err)
	}

	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(reply, "router-1") {
		t.Errorf("get-config reply missing data: %s", reply)
	}
	if !strings.Contains(reply, `message-id="1"`) {
		t.Errorf("reply missing message-id: %s", reply)
	}

	if err := writeMsg(client, &struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`
		MessageID string   `xml:"message-id,attr"`
		Op        string   `xml:"close-session"`
	}{MessageID: "2"}); err != nil {
		t.Fatalf("send close-session: %v", err)
	}

	closeReply, _ := readRaw(client)
	if !strings.Contains(closeReply, "<ok/>") {
		t.Errorf("close-session reply missing <ok/>: %s", closeReply)
	}

	if err := <-done; err != nil {
		t.Fatalf("server loop: %v", err)
	}
}

func TestServerLoop_UnknownOp(t *testing.T) {
	client, server := NewPipe()
	caps := []string{netconf.CapNetConf10}
	handlers := map[string]Handler{
		"close-session": func(msgID string, inner []byte) (any, error) {
			return []byte("<ok/>"), nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- ServerLoop(server, handlers, caps, 1, "test")
	}()

	_, _ = readMsg[netconf.Hello](client)
	_ = writeMsg(client, &netconf.Hello{Capabilities: []string{netconf.CapNetConf10}})

	_ = writeMsg(client, &struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`
		MessageID string   `xml:"message-id,attr"`
		Op        string   `xml:"bogus"`
	}{MessageID: "9"})

	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(reply, "operation-not-supported") {
		t.Errorf("expected operation-not-supported: %s", reply)
	}

	_ = writeMsg(client, &struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`
		MessageID string   `xml:"message-id,attr"`
		Op        string   `xml:"close-session"`
	}{MessageID: "10"})
	_, _ = readRaw(client)

	client.Close()
	<-done
}

func TestServerLoop_HandlerError(t *testing.T) {
	client, server := NewPipe()
	caps := []string{netconf.CapNetConf10}
	handlers := map[string]Handler{
		"bad": func(msgID string, inner []byte) (any, error) {
			return nil, &netconf.RPCError{
				Tag:      netconf.ErrBadElement,
				Severity: netconf.SevError,
				Message:  "nope",
			}
		},
		"close-session": func(msgID string, inner []byte) (any, error) {
			return []byte("<ok/>"), nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- ServerLoop(server, handlers, caps, 1, "test")
	}()

	_, _ = readMsg[netconf.Hello](client)
	_ = writeMsg(client, &netconf.Hello{Capabilities: []string{netconf.CapNetConf10}})

	_ = writeMsg(client, &struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`
		MessageID string   `xml:"message-id,attr"`
		Op        string   `xml:"bad"`
	}{MessageID: "1"})

	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(reply, "bad-element") || !strings.Contains(reply, "nope") {
		t.Errorf("expected bad-element error: %s", reply)
	}

	_ = writeMsg(client, &struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`
		MessageID string   `xml:"message-id,attr"`
		Op        string   `xml:"close-session"`
	}{MessageID: "2"})
	_, _ = readRaw(client)

	client.Close()
	<-done
}

func readRaw(tr interface{ MsgReader() (io.ReadCloser, error) }) (string, error) {
	r, err := tr.MsgReader()
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
