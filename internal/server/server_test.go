package server

import (
	"context"
	"encoding/xml"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nemith.io/netconf"
	nemithtransport "nemith.io/netconf/transport"

	"github.com/example/confd/internal/sysrepoadapter"
)

func yangDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "yang"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// writeManifest creates a temp plugins.yaml pointing at the in-tree yang/
// directory and returns its path. This is used by tests to load YANG modules
// via the YANG manifest path instead of the old --yang-path.
func writeManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.yaml")
	content := "plugins:\n  - name: confd-test\n    yang_dir: \"" + yangDir(t) + "\"\n    modules:\n      - confd-test.yang\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// sampleTree builds a small in-memory data tree for the confd-test module.
func sampleTree() *sysrepoadapter.DataNode {
	ns := "urn:ietf:params:xml:ns:yang:confd-test"
	return &sysrepoadapter.DataNode{XPath: "/", Name: "root", Children: []*sysrepoadapter.DataNode{
		{XPath: "/confd-test:system", Name: "system", NS: ns, Children: []*sysrepoadapter.DataNode{
			{XPath: "/confd-test:system/hostname", Name: "hostname", NS: ns, IsLeaf: true, Value: "router-1"},
			{XPath: "/confd-test:system/interfaces", Name: "interfaces", NS: ns, Children: []*sysrepoadapter.DataNode{
				{XPath: "/confd-test:system/interfaces/interface[name='eth0']", Name: "interface", NS: ns, IsList: true, Key: "eth0", Children: []*sysrepoadapter.DataNode{
					{Name: "name", NS: ns, IsLeaf: true, Value: "eth0"},
					{Name: "mtu", NS: ns, IsLeaf: true, Value: "1500"},
					{Name: "enabled", NS: ns, IsLeaf: true, Value: "true"},
				}},
			}},
		}},
		{XPath: "/confd-test:state", Name: "state", NS: ns, Children: []*sysrepoadapter.DataNode{
			{Name: "uptime-seconds", NS: ns, IsLeaf: true, Value: "86400"},
			{Name: "load-average", NS: ns, IsLeaf: true, Value: "1.23"},
		}},
	}}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	mock := sysrepoadapter.NewMock(nil)
	srv, err := New(context.Background(), Config{
		Adapter:      mock,
		YangManifest: writeManifest(t),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	mock.SetData(sysrepoadapter.Running, sampleTree())
	mock.SetData(sysrepoadapter.Operational, sampleTree())
	return srv
}

// pipeSession is an in-memory test transport that satisfies
// transport.SessionInterface. It wraps a net.Pipe connection with
// nemith's Framer.
type pipeSession struct {
	framer *nemithtransport.Framer
	conn   net.Conn
}

func (s *pipeSession) MsgReader() (io.ReadCloser, error) { return s.framer.MsgReader() }
func (s *pipeSession) MsgWriter() (io.WriteCloser, error) { return s.framer.MsgWriter() }
func (s *pipeSession) Upgrade()                           { s.framer.Upgrade() }
func (s *pipeSession) Close() error                        { return s.conn.Close() }
func (s *pipeSession) PeerUser() string                   { return "test" }

// newPipe returns a pair of connected pipeSessions (client, server).
func newPipe() (*pipeSession, *pipeSession) {
	ca, cb := net.Pipe()
	client := &pipeSession{framer: nemithtransport.NewFramer(ca, ca), conn: ca}
	server := &pipeSession{framer: nemithtransport.NewFramer(cb, cb), conn: cb}
	return client, server
}

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

// clientHandshake reads the server <hello>, sends a client <hello>
// advertising base:1.1, and upgrades to chunked framing.
func clientHandshake(t *testing.T, tr *pipeSession) {
	t.Helper()
	hello, err := readMsg[netconf.Hello](tr)
	if err != nil {
		t.Fatalf("read server hello: %v", err)
	}
	if !strings.Contains(strings.Join(hello.Capabilities, ","), netconf.CapNetConf11) {
		t.Errorf("server hello missing base:1.1: %s", hello.Capabilities)
	}
	if err := writeMsg(tr, &netconf.Hello{
		Capabilities: []string{netconf.CapNetConf10, netconf.CapNetConf11},
	}); err != nil {
		t.Fatalf("send client hello: %v", err)
	}
	tr.Upgrade()
}

type rpcStruct struct {
	XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`
	MessageID string   `xml:"message-id,attr"`
	Inner     string   `xml:",innerxml"`
}

func sendRPC(tr *pipeSession, msgID, op string) error {
	return writeMsg(tr, &rpcStruct{MessageID: msgID, Inner: "<" + op + "/>"})
}

func sendRPCBody(tr *pipeSession, msgID, body string) error {
	return writeMsg(tr, &rpcStruct{MessageID: msgID, Inner: body})
}

// startServerPipe creates a test server, a transport pipe, and starts
// ServeTransport on the server side. Returns the client pipe transport.
func startServerPipe(t *testing.T) (*Server, *pipeSession, chan error) {
	t.Helper()
	srv := newTestServer(t)
	client, server := newPipe()
	done := make(chan error, 1)
	go func() { done <- srv.ServeTransport(context.Background(), server) }()
	return srv, client, done
}

func TestServer_GetConfig(t *testing.T) {
	srv, client, _ := startServerPipe(t)
	defer srv.Close()

	clientHandshake(t, client)

	if err := sendRPCBody(client, "101", "<get-config><source><running/></source></get-config>"); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(reply, "router-1") {
		t.Errorf("get-config reply missing data: %s", reply)
	}
	if !strings.Contains(reply, `message-id="101"`) {
		t.Errorf("reply missing message-id: %s", reply)
	}
	_ = sendRPC(client, "999", "close-session")
	_, _ = readRaw(client)
}

func TestServer_Get_Operational(t *testing.T) {
	srv, client, _ := startServerPipe(t)
	defer srv.Close()
	clientHandshake(t, client)

	if err := sendRPC(client, "202", "get"); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(reply, "uptime-seconds") {
		t.Errorf("get reply missing state: %s", reply)
	}
	_ = sendRPC(client, "999", "close-session")
	_, _ = readRaw(client)
}

func TestServer_GetSchema(t *testing.T) {
	srv, client, _ := startServerPipe(t)
	defer srv.Close()
	clientHandshake(t, client)

	if err := sendRPCBody(client, "303", "<get-schema><identifier>confd-test</identifier></get-schema>"); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rp struct {
		XMLName xml.Name `xml:"rpc-reply"`
		Data    string   `xml:"data"`
	}
	if err := xml.Unmarshal([]byte(reply), &rp); err != nil {
		t.Fatalf("parse reply: %v\n%s", err, reply)
	}
	if !strings.Contains(rp.Data, "module confd-test") {
		t.Errorf("get-schema returned wrong content: %q", rp.Data)
	}
	_ = sendRPC(client, "999", "close-session")
	_, _ = readRaw(client)
}

func TestServer_UnknownOp(t *testing.T) {
	srv, client, _ := startServerPipe(t)
	defer srv.Close()
	clientHandshake(t, client)

	if err := sendRPC(client, "404", "bogus-op"); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(reply, "operation-not-supported") {
		t.Errorf("expected operation-not-supported: %s", reply)
	}
	_ = sendRPC(client, "999", "close-session")
	_, _ = readRaw(client)
}

func TestServer_LockAndUnlock(t *testing.T) {
	srv, client, _ := startServerPipe(t)
	defer srv.Close()
	clientHandshake(t, client)

	if err := sendRPCBody(client, "1", "<lock><target><running/></target></lock>"); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if !strings.Contains(reply, "<ok/>") {
		t.Errorf("lock reply: %s", reply)
	}
	if err := sendRPCBody(client, "2", "<unlock><target><running/></target></unlock>"); err != nil {
		t.Fatalf("write unlock: %v", err)
	}
	reply2, err := readRaw(client)
	if err != nil {
		t.Fatalf("read unlock: %v", err)
	}
	if !strings.Contains(reply2, "<ok/>") {
		t.Errorf("unlock reply: %s", reply2)
	}
	_ = sendRPC(client, "999", "close-session")
	_, _ = readRaw(client)
}

func TestServer_CloseSession(t *testing.T) {
	srv, client, done := startServerPipe(t)
	defer srv.Close()
	clientHandshake(t, client)

	if err := sendRPC(client, "9", "close-session"); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := readRaw(client)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(reply, "<ok/>") {
		t.Errorf("close-session reply: %s", reply)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("server did not close session after close-session")
	}
}
