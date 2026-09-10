package server

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/confd/internal/framing"
	"github.com/example/confd/internal/hello"
	"github.com/example/confd/internal/sysrepoadapter"
	"github.com/example/confd/internal/transport"
)

func yangDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "yang"))
	if err != nil {
		t.Fatal(err)
	}
	return d
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
		YANGPaths: []string{yangDir(t)},
		Adapter:   mock,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	mock.SetData(sysrepoadapter.Running, sampleTree())
	mock.SetData(sysrepoadapter.Operational, sampleTree())
	return srv
}

// clientHandshake reads the server <hello>, replies with a base:1.1
// <hello>, and upgrades the client framing to base:1.1.
func clientHandshake(t *testing.T, r *framing.Reader, w *framing.Writer) {
	t.Helper()
	helloBytes, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if !strings.Contains(string(helloBytes), "base:1.1") {
		t.Errorf("server hello missing base:1.1: %s", helloBytes)
	}
	if err := w.WriteMessage([]byte(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>` + hello.Base11 + `</capability></capabilities></hello>`)); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	r.Upgrade()
	w.Upgrade()
}

func TestServer_GetConfig(t *testing.T) {
	srv := newTestServer(t)
	client, server := transport.Pipe()
	go srv.ServeTransport(context.Background(), server)

	r, w := client.Framing()
	clientHandshake(t, r, w)

	rpc1 := []byte(`<rpc message-id="101" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-config><source><running/></source></get-config></rpc>`)
	if err := w.WriteMessage(rpc1); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
	reply, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(string(reply), "router-1") {
		t.Errorf("get-config reply missing data: %s", reply)
	}
	if !strings.Contains(string(reply), `message-id="101"`) {
		t.Errorf("reply missing message-id: %s", reply)
	}
}

func TestServer_Get_Operational(t *testing.T) {
	srv := newTestServer(t)
	client, server := transport.Pipe()
	go srv.ServeTransport(context.Background(), server)
	r, w := client.Framing()
	clientHandshake(t, r, w)

	if err := w.WriteMessage([]byte(`<rpc message-id="202" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get/></rpc>`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(reply), "uptime-seconds") {
		t.Errorf("get reply missing state: %s", reply)
	}
}

func TestServer_GetSchema(t *testing.T) {
	srv := newTestServer(t)
	client, server := transport.Pipe()
	go srv.ServeTransport(context.Background(), server)
	r, w := client.Framing()
	clientHandshake(t, r, w)

	if err := w.WriteMessage([]byte(`<rpc message-id="303" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-schema><identifier>confd-test</identifier></get-schema></rpc>`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := r.ReadMessage()
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
}

func TestServer_UnknownOp(t *testing.T) {
	srv := newTestServer(t)
	client, server := transport.Pipe()
	go srv.ServeTransport(context.Background(), server)
	r, w := client.Framing()
	clientHandshake(t, r, w)

	if err := w.WriteMessage([]byte(`<rpc message-id="404" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><edit-config/></rpc>`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(reply), "operation-not-supported") {
		t.Errorf("expected operation-not-supported: %s", reply)
	}
}

func TestServer_LockAndUnlock(t *testing.T) {
	srv := newTestServer(t)
	client, server := transport.Pipe()
	go srv.ServeTransport(context.Background(), server)
	r, w := client.Framing()
	clientHandshake(t, r, w)

	if err := w.WriteMessage([]byte(`<rpc message-id="1" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><lock><target><running/></target></lock></rpc>`)); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	reply, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if !strings.Contains(string(reply), "<ok/>") {
		t.Errorf("lock reply: %s", reply)
	}
	if err := w.WriteMessage([]byte(`<rpc message-id="2" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><unlock><target><running/></target></unlock></rpc>`)); err != nil {
		t.Fatalf("write unlock: %v", err)
	}
	reply2, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read unlock: %v", err)
	}
	if !strings.Contains(string(reply2), "<ok/>") {
		t.Errorf("unlock reply: %s", reply2)
	}
}

func TestServer_CloseSession(t *testing.T) {
	srv := newTestServer(t)
	client, server := transport.Pipe()
	done := make(chan error, 1)
	go func() { done <- srv.ServeTransport(context.Background(), server) }()
	r, w := client.Framing()
	clientHandshake(t, r, w)

	if err := w.WriteMessage([]byte(`<rpc message-id="9" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><close-session/></rpc>`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(reply), "<ok/>") {
		t.Errorf("close-session reply: %s", reply)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("server did not close session after close-session")
	}
}
