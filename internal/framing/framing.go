// Package framing implements RFC 6242 NETCONF message chunked framing
// (and the base:1.0 EOM fallback `\n]]>]]>\n`).
package framing

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

const (
	// RFC 6242: maximum chunk size is 4 hex digits => 0xffff.
	MaxChunkSize = 0xffff

	// EOM1 is the base:1.0 end-of-message marker (with surrounding newlines).
	EOM1 = "\n]]>]]>\n"
)

// Mode selects the framing variant in use for a session.
type Mode int

const (
	// ModeBase10 uses the `\n]]>]]>\n` end-of-message marker from RFC 4742.
	ModeBase10 Mode = iota
	// ModeBase11 uses RFC 6242 chunked framing.
	ModeBase11
)

// Reader reads whole NETCONF messages from an underlying byte stream.
type Reader struct {
	r       *bufio.Reader
	mode    Mode
	started bool
}

// NewReader returns a Reader that defaults to base:1.0 framing until
// Upgrade is called. This matches the NETCONF 1.0/1.1 negotiation flow:
// both peers begin in 1.0 and switch to 1.1 only after exchanging <hello>.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r), mode: ModeBase10}
}

// Upgrade switches the reader to RFC 6242 chunked framing.
func (r *Reader) Upgrade() { r.mode = ModeBase11 }

// Mode returns the currently active framing mode.
func (r *Reader) Mode() Mode { return r.mode }

// ReadMessage reads one complete NETCONF <message> ... EOM and returns its
// raw bytes (without the framing markers). io.EOF is returned only when the
// underlying stream reports a clean EOF at a message boundary.
func (r *Reader) ReadMessage() ([]byte, error) {
	if r.mode == ModeBase10 {
		return r.readBase10()
	}
	return r.readBase11()
}

func (r *Reader) readBase10() ([]byte, error) {
	// base:1.0 messages are delimited by the literal `]]>]]>` marker
	// (RFC 4742 §4.3). We accumulate lines until the marker appears at the
	// end of the buffer, then strip it (and any preceding newline) and
	// return the message body.
	const marker = "]]>]]>"
	var buf []byte
	for {
		line, err := r.r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			if len(buf) == 0 && len(line) == 0 {
				return nil, err
			}
			return nil, err
		}
		buf = append(buf, line...)
		trimmed := bytes.TrimRight(buf, "\r\n")
		if bytes.HasSuffix(trimmed, []byte(marker)) {
			body := trimmed[:len(trimmed)-len(marker)]
			body = bytes.TrimSuffix(body, []byte("\n"))
			return body, nil
		}
		if errors.Is(err, io.EOF) {
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		}
	}
}

func (r *Reader) readBase11() ([]byte, error) {
	var msg []byte
	for {
		// Read the leading '\n'.
		b, err := r.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(msg) == 0 && !r.started {
					return nil, io.EOF
				}
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if b != '\n' {
			return nil, fmt.Errorf("framing: expected '\\n' before chunk, got %q", b)
		}
		r.started = true

		// Read the chunk header up to and including the second '\n'.
		header, err := r.r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("framing: short chunk header: %w", err)
		}
		header = header[:len(header)-1] // strip trailing '\n'

		// EOM marker: `\n##\n`. Tolerate `\n#\n` as well.
		if header == "#" || header == "##" {
			r.started = false
			return msg, nil
		}
		if len(header) < 2 || header[0] != '#' {
			return nil, fmt.Errorf("framing: bad chunk header %q", header)
		}
		hexLen := header[1:] // strip leading '#'
		l, ok := parseHexLen(hexLen)
		if !ok {
			return nil, fmt.Errorf("framing: bad chunk length %q", hexLen)
		}
		if l < 0 || l > MaxChunkSize {
			return nil, fmt.Errorf("framing: chunk length %d out of range", l)
		}
		buf := make([]byte, l)
		if _, err := io.ReadFull(r.r, buf); err != nil {
			return nil, fmt.Errorf("framing: short chunk payload: %w", err)
		}
		msg = append(msg, buf...)
	}
}

func parseHexLen(s string) (int, bool) {
	if len(s) == 0 || len(s)%2 != 0 || len(s) > 4 {
		return 0, false
	}
	l := 0
	for _, c := range []byte(s) {
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'f':
			v = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = int(c-'A') + 10
		default:
			return 0, false
		}
		l = l<<4 | v
	}
	return l, true
}

// Writer writes whole NETCONF messages using RFC 6242 chunked framing
// (or base:1.0 EOM when in ModeBase10).
type Writer struct {
	w    *bufio.Writer
	mode Mode
}

// NewWriter returns a Writer starting in base:1.0 mode.
func NewWriter(w io.Writer) *Writer { return &Writer{w: bufio.NewWriter(w)} }

// Upgrade switches the writer to RFC 6242 chunked framing.
func (w *Writer) Upgrade() { w.mode = ModeBase11 }

// Mode returns the currently active framing mode.
func (w *Writer) Mode() Mode { return w.mode }

// WriteMessage frames and writes one complete NETCONF message.
func (w *Writer) WriteMessage(msg []byte) error {
	if w.mode == ModeBase10 {
		if _, err := w.w.Write(msg); err != nil {
			return err
		}
		if _, err := w.w.WriteString(EOM1); err != nil {
			return err
		}
		return w.w.Flush()
	}
	for len(msg) > 0 {
		n := len(msg)
		if n > MaxChunkSize {
			n = MaxChunkSize
		}
		if _, err := w.w.WriteString(fmt.Sprintf("\n#%04x\n", n)); err != nil {
			return err
		}
		if _, err := w.w.Write(msg[:n]); err != nil {
			return err
		}
		msg = msg[n:]
	}
	if _, err := w.w.WriteString("\n##\n"); err != nil {
		return err
	}
	return w.w.Flush()
}
