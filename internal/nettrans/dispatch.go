package nettrans

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"nemith.io/netconf"
)

// Handler processes a single parsed <rpc> and returns either a reply body
// (any XML-marshalable value) or an error (which will be encoded as an
// <rpc-error>).
type Handler func(msgID string, innerXML []byte) (any, error)

// rpcEnvelope is used to decode incoming <rpc> messages. We can't use
// netconf.RPC directly because its Operation field is `any` with
// `xml:",innerxml"`, which doesn't populate with `any` — only `[]byte`.
type rpcEnvelope struct {
	XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`
	MessageID string   `xml:"message-id,attr"`
	InnerXML  []byte   `xml:",innerxml"`
}

// ServerLoop implements the server-side NETCONF protocol:
//  1. Send <hello> with the given capabilities + session-id.
//  2. Read peer <hello>; upgrade to base:1.1 if both sides support it.
//  3. Loop: read framed messages → decode <rpc> → dispatch → write <rpc-reply>.
//  4. On <close-session> or transport EOF → return.
//
// tr must implement transport.Transport (MsgReader/MsgWriter/Close).
// If tr also implements interface{ Upgrade() }, it will be upgraded after
// base:1.1 negotiation.
func ServerLoop(
	tr interface {
		io.Closer
		MsgReader() (io.ReadCloser, error)
		MsgWriter() (io.WriteCloser, error)
	},
	handlers map[string]Handler,
	serverCaps []string,
	sessionID uint64,
	peerUser string,
) error {
	defer tr.Close()

	// --- <hello> phase ---------------------------------------------------
	// Build + send server hello.
	hello := netconf.Hello{
		SessionID:    sessionID,
		Capabilities: serverCaps,
	}
	if err := writeMsg(tr, &hello); err != nil {
		return fmt.Errorf("nettrans: send hello: %w", err)
	}

	// Read peer hello.
	peerHello, err := readMsg[netconf.Hello](tr)
	if err != nil {
		return fmt.Errorf("nettrans: read peer hello: %w", err)
	}

	// Negotiate base:1.1.
	peerCaps := netconf.NewCapabilitySet(peerHello.Capabilities...)
	ourCaps := netconf.NewCapabilitySet(serverCaps...)
	if peerCaps.Has(netconf.CapNetConf11) && ourCaps.Has(netconf.CapNetConf11) {
		if up, ok := tr.(interface{ Upgrade() }); ok {
			up.Upgrade()
		}
	}

	// --- <rpc> phase ----------------------------------------------------
	for {
		msgReader, err := tr.MsgReader()
		if err != nil {
			return nil // transport closed → clean exit
		}

		// Decode the <rpc> envelope.
		var rpc rpcEnvelope
		if err := xml.NewDecoder(msgReader).Decode(&rpc); err != nil {
			_ = msgReader.Close()
			if err == io.EOF || strings.Contains(err.Error(), "EOF") {
				return nil
			}
			// Send a malformed-message error and continue.
			writeErrorReply(tr, "", netconf.RPCError{
				Tag:      netconf.ErrMalformedMessage,
				Severity: netconf.SevError,
				Message:  err.Error(),
			})
			continue
		}
		_ = msgReader.Close()

		// Extract the operation name from the inner XML.
		opName, err := extractOpName(rpc.InnerXML)
		if err != nil {
			writeErrorReply(tr, rpc.MessageID, netconf.RPCError{
				Tag:      netconf.ErrMalformedMessage,
				Severity: netconf.SevError,
				Message:  err.Error(),
			})
			continue
		}

		// Dispatch.
		handler, ok := handlers[opName]
		if !ok {
			writeErrorReply(tr, rpc.MessageID, netconf.RPCError{
				Tag:      netconf.ErrOperationNotSupported,
				Severity: netconf.SevError,
				Message:  fmt.Sprintf("operation '%s' is not supported", opName),
			})
			continue
		}

		reply, herr := handler(rpc.MessageID, rpc.InnerXML)
		if herr != nil {
			if rce, ok := herr.(*netconf.RPCError); ok {
				writeErrorReply(tr, rpc.MessageID, *rce)
			} else {
				writeErrorReply(tr, rpc.MessageID, netconf.RPCError{
					Tag:      netconf.ErrOperationFailed,
					Severity: netconf.SevError,
					Message:  herr.Error(),
				})
			}
			continue
		}

		if err := writeReply(tr, rpc.MessageID, reply); err != nil {
			return fmt.Errorf("nettrans: write reply: %w", err)
		}

		if opName == "close-session" {
			return nil
		}
	}
}

// writeMsg encodes a value as a framed message.
func writeMsg(tr interface{ MsgWriter() (io.WriteCloser, error) }, v any) error {
	w, err := tr.MsgWriter()
	if err != nil {
		return err
	}
	if err := xml.NewEncoder(w).Encode(v); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// readMsg reads and decodes a framed message.
func readMsg[T any](tr interface{ MsgReader() (io.ReadCloser, error) }) (T, error) {
	var zero T
	r, err := tr.MsgReader()
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

// writeReply writes a <rpc-reply> with the given message-id and body.
func writeReply(tr interface{ MsgWriter() (io.WriteCloser, error) }, msgID string, body any) error {
	type replyEnvelope struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc-reply"`
		MessageID string   `xml:"message-id,attr"`
		Body      any      `xml:",innerxml"`
	}
	return writeMsg(tr, &replyEnvelope{MessageID: msgID, Body: body})
}

// writeErrorReply writes a <rpc-reply> containing an <rpc-error>.
func writeErrorReply(tr interface{ MsgWriter() (io.WriteCloser, error) }, msgID string, rce netconf.RPCError) {
	type errEnvelope struct {
		XMLName   xml.Name          `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc-reply"`
		MessageID string            `xml:"message-id,attr"`
		Error     netconf.RPCError  `xml:"rpc-error"`
	}
	_ = writeMsg(tr, &errEnvelope{MessageID: msgID, Error: rce})
}

// extractOpName parses the inner XML of an <rpc> to find the first start
// element's local name (the operation name).
func extractOpName(innerXML []byte) (string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(innerXML)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("invalid rpc operation: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local, nil
		}
	}
}
