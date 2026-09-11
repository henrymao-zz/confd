package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/example/confd/internal/framing"
	"golang.org/x/crypto/ssh"
)

// sshListener is a Listener backed by an SSH server over TCP.
type sshListener struct {
	tcp net.Listener
	cfg *ssh.ServerConfig
}

// SSHConfig configures the SSH transport.
type SSHConfig struct {
	Bind        string
	HostKeyPath string // optional; if empty an in-memory ed25519 key is used
	Username    string // optional; if empty any authenticated user is accepted
	Password    string // optional; if set, password auth is enabled
}

// NewSSH returns a Listener that accepts NETCONF-over-SSH sessions.
func NewSSH(cfg SSHConfig) (Listener, error) {
	hostSigner, err := loadOrGenerateHostKey(cfg.HostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("transport: ssh host key: %w", err)
	}
	sshCfg := &ssh.ServerConfig{}
	sshCfg.AddHostKey(hostSigner)

	if cfg.Password != "" {
		pw := cfg.Password
		sshCfg.PasswordCallback = func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if cfg.Username != "" && c.User() != cfg.Username {
				return nil, errors.New("transport: bad username")
			}
			if string(pass) != pw {
				return nil, errors.New("transport: bad password")
			}
			return nil, nil
		}
	} else {
		// No configured auth: accept any username with no credentials. This
		// matches the "noauth" mode used by integration tests; production
		// deployments should configure password/publickey auth.
		sshCfg.PasswordCallback = func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			return nil, nil
		}
	}

	tcp, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		return nil, fmt.Errorf("transport: listen %s: %w", cfg.Bind, err)
	}
	return &sshListener{tcp: tcp, cfg: sshCfg}, nil
}

func (l *sshListener) Accept() (Transport, error) {
	for {
		conn, err := l.tcp.Accept()
		if err != nil {
			return nil, err
		}
		sshConn, chans, reqs, err := ssh.NewServerConn(conn, l.cfg)
		if err != nil {
			// Keep accepting on next connection.
			continue
		}
		go ssh.DiscardRequests(reqs)
		return &sshTransport{
			conn:  sshConn,
			chans: chans,
			user:  sshConn.User(),
		}, nil
	}
}

func (l *sshListener) Close() error { return l.tcp.Close() }

func (l *sshListener) Addr() net.Addr { return l.tcp.Addr() }

// sshTransport wraps an SSH session and yields the first "session" channel
// as a NETCONF transport.
type sshTransport struct {
	conn   ssh.Conn
	chans  <-chan ssh.NewChannel
	user   string
	r      *framing.Reader
	w      *framing.Writer
	ch     ssh.Channel
	closed bool
}

func (t *sshTransport) ReadMessage() ([]byte, error) {
	if err := t.ensure(); err != nil {
		return nil, err
	}
	return t.r.ReadMessage()
}

func (t *sshTransport) WriteMessage(msg []byte) error {
	if err := t.ensure(); err != nil {
		return err
	}
	return t.w.WriteMessage(msg)
}

func (t *sshTransport) Framing() (*framing.Reader, *framing.Writer) {
	_ = t.ensure()
	return t.r, t.w
}

func (t *sshTransport) RawChannel() (io.Reader, io.Writer) {
	_ = t.ensure()
	return t.ch, t.ch
}

func (t *sshTransport) PeerUser() string { return t.user }

func (t *sshTransport) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	if t.ch != nil {
		_ = t.ch.Close()
	}
	if t.conn != nil {
		_ = t.conn.Close()
	}
	return nil
}

func (t *sshTransport) ensure() error {
	if t.r != nil && t.w != nil {
		return nil
	}
	return t.acceptChannel()
}

func (t *sshTransport) acceptChannel() error {
	for newChan := range t.chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, reqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		// Handle channel-level requests (subsystem, exec, shell, env,
		// pty-req, etc.) so the SSH client doesn't see "subsystem request
		// failed". We accept "subsystem" (used by `ssh -s ... netconf`)
		// and "exec" and reject everything else.
		go t.handleChannelRequests(reqs, ch)
		t.ch = ch
		t.r = framing.NewReader(ch)
		t.w = framing.NewWriter(ch)
		return nil
	}
	return io.EOF
}

// handleChannelRequests processes per-channel SSH requests. The
// "subsystem" request (sent by `ssh -s host netconf`) is accepted so
// the client doesn't report "subsystem request failed".
func (t *sshTransport) handleChannelRequests(reqs <-chan *ssh.Request, ch ssh.Channel) {
	for req := range reqs {
		switch req.Type {
		case "subsystem":
			// Parse the subsystem name (4-byte length + string).
			ok := true
			if len(req.Payload) < 4 {
				ok = false
			}
			if ok {
				// Accept any subsystem name; we're a NETCONF server.
				_ = req.Reply(true, nil)
				continue
			}
			_ = req.Reply(false, nil)
		case "exec":
			_ = req.Reply(true, nil)
		default:
			// Reject unknown requests (shell, pty-req, env, etc.).
			_ = req.Reply(false, nil)
		}
	}
}

func loadOrGenerateHostKey(path string) (ssh.Signer, error) {
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("transport: read host key: %w", err)
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, errors.New("transport: host key is not PEM-encoded")
		}
		return ssh.ParsePrivateKey(raw)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	_ = pub
	return ssh.NewSignerFromSigner(priv)
}
