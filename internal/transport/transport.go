// Package transport provides the SSH transport layer and the server-side
// NETCONF protocol loop for confd. Framing (RFC 6242) is handled by
// nemith.io/netconf/transport.Framer, embedded directly into the Session
// type returned by the SSH listener.
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

	nemithtransport "nemith.io/netconf/transport"
	"golang.org/x/crypto/ssh"
)

// Listener is a NETCONF transport listener.
type Listener interface {
	Accept() (*Session, error)
	Close() error
	Addr() net.Addr
}

// SessionInterface is the interface that ServerLoop expects. Both
// *Session (SSH) and *PipeTransport (tests) satisfy it.
type SessionInterface interface {
	MsgReader() (io.ReadCloser, error)
	MsgWriter() (io.WriteCloser, error)
	Upgrade()
	PeerUser() string
	Close() error
}

// Session is a NETCONF SSH session with nemith framing already attached.
// It implements the interface that ServerLoop expects (MsgReader/MsgWriter/
// Upgrade/Close) plus PeerUser for NACM/audit.
type Session struct {
	framer *nemithtransport.Framer
	user   string
	conn   *ssh.ServerConn
	ch     ssh.Channel
}

func (s *Session) MsgReader() (io.ReadCloser, error) { return s.framer.MsgReader() }
func (s *Session) MsgWriter() (io.WriteCloser, error) { return s.framer.MsgWriter() }
func (s *Session) Upgrade()                           { s.framer.Upgrade() }
func (s *Session) PeerUser() string                   { return s.user }

func (s *Session) Close() error {
	_ = s.ch.Close()
	_ = s.conn.Close()
	return nil
}

// SSHConfig configures the SSH transport.
type SSHConfig struct {
	Bind        string
	HostKeyPath string
	Username    string
	Password    string
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

type sshListener struct {
	tcp net.Listener
	cfg *ssh.ServerConfig
}

func (l *sshListener) Accept() (*Session, error) {
	for {
		conn, err := l.tcp.Accept()
		if err != nil {
			return nil, err
		}
		sshConn, chans, reqs, err := ssh.NewServerConn(conn, l.cfg)
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(reqs)

		ch, err := acceptSessionChannel(chans)
		if err != nil {
			_ = sshConn.Close()
			continue
		}

		return &Session{
			framer: nemithtransport.NewFramer(ch, ch),
			user:   sshConn.User(),
			conn:   sshConn,
			ch:     ch,
		}, nil
	}
}

func (l *sshListener) Close() error { return l.tcp.Close() }
func (l *sshListener) Addr() net.Addr { return l.tcp.Addr() }

// acceptSessionChannel accepts the first "session" channel and handles
// the "subsystem" request that `ssh -s ... netconf` sends.
func acceptSessionChannel(chans <-chan ssh.NewChannel) (ssh.Channel, error) {
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, reqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go handleChannelRequests(reqs)
		return ch, nil
	}
	return nil, io.EOF
}

func handleChannelRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "subsystem":
			if len(req.Payload) >= 4 {
				_ = req.Reply(true, nil)
				continue
			}
			_ = req.Reply(false, nil)
		case "exec":
			_ = req.Reply(true, nil)
		default:
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
