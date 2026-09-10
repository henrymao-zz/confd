package operations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/confd/internal/data"
	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/schema"
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

func seedTree() *sysrepoadapter.DataNode {
	ns := "urn:ietf:params:xml:ns:yang:confd-test"
	return &sysrepoadapter.DataNode{XPath: "/", Name: "root", Children: []*sysrepoadapter.DataNode{
		{XPath: "/confd-test:system", Name: "system", NS: ns, Children: []*sysrepoadapter.DataNode{
			{XPath: "/confd-test:system/hostname", Name: "hostname", NS: ns, IsLeaf: true, Value: "op-router"},
		}},
	}}
}

func newDeps(t *testing.T) (Deps, *sysrepoadapter.Mock) {
	t.Helper()
	cache := schema.New()
	if err := cache.LoadDirectory(yangDir(t)); err != nil {
		t.Fatalf("load: %v", err)
	}
	mock := sysrepoadapter.NewMock(nil)
	mock.SetData(sysrepoadapter.Running, seedTree())
	mock.SetData(sysrepoadapter.Operational, seedTree())
	conn, _ := mock.Connect(context.Background())
	return Deps{
		Cache:    cache,
		Conn:     conn,
		Encoder:  data.New(cache),
		Sessions: NewSessionRegistry(),
	}, mock
}

func TestOperations_GetConfig(t *testing.T) {
	deps, _ := newDeps(t)
	d := rpc.NewDispatcher()
	Register(d, deps)
	out := d.Handle(rpc.Context{}, []byte(`<rpc message-id="1" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-config><source><running/></source></get-config></rpc>`))
	if !strings.Contains(string(out), "op-router") {
		t.Errorf("get-config reply: %s", out)
	}
	if !strings.Contains(string(out), `<data>`) {
		t.Errorf("missing <data>: %s", out)
	}
}

func TestOperations_Get_BadSource(t *testing.T) {
	deps, _ := newDeps(t)
	d := rpc.NewDispatcher()
	Register(d, deps)
	out := d.Handle(rpc.Context{}, []byte(`<rpc message-id="2" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-config><source><bogus/></source></get-config></rpc>`))
	if !strings.Contains(string(out), "invalid-value") {
		t.Errorf("expected invalid-value: %s", out)
	}
}

func TestOperations_GetSchema_Missing(t *testing.T) {
	deps, _ := newDeps(t)
	d := rpc.NewDispatcher()
	Register(d, deps)
	out := d.Handle(rpc.Context{}, []byte(`<rpc message-id="3" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-schema><identifier>no-such-module</identifier></get-schema></rpc>`))
	if !strings.Contains(string(out), "not found") {
		t.Errorf("expected not-found error: %s", out)
	}
}

func TestOperations_GetSchema_VersionMismatch(t *testing.T) {
	deps, _ := newDeps(t)
	d := rpc.NewDispatcher()
	Register(d, deps)
	out := d.Handle(rpc.Context{}, []byte(`<rpc message-id="4" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-schema><identifier>confd-test</identifier><version>1999-01-01</version></get-schema></rpc>`))
	if !strings.Contains(string(out), "invalid-value") {
		t.Errorf("expected invalid-value: %s", out)
	}
}

func TestOperations_LockDenied(t *testing.T) {
	deps, _ := newDeps(t)
	// Pre-lock running via a first session.
	s1, _ := deps.Conn.OpenSession(context.Background(), "u1")
	_ = s1.Lock(sysrepoadapter.Running)
	d := rpc.NewDispatcher()
	Register(d, deps)
	out := d.Handle(rpc.Context{}, []byte(`<rpc message-id="5" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><lock><target><running/></target></lock></rpc>`))
	if !strings.Contains(string(out), "lock-denied") {
		t.Errorf("expected lock-denied: %s", out)
	}
}

func TestOperations_KillSession_NotFound(t *testing.T) {
	deps, _ := newDeps(t)
	d := rpc.NewDispatcher()
	Register(d, deps)
	out := d.Handle(rpc.Context{SessionID: 1}, []byte(`<rpc message-id="6" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><kill-session><session-id>999</session-id></kill-session></rpc>`))
	if !strings.Contains(string(out), "not found") {
		t.Errorf("expected not-found: %s", out)
	}
}

func TestOperations_CloseSession(t *testing.T) {
	deps, _ := newDeps(t)
	d := rpc.NewDispatcher()
	Register(d, deps)
	out := d.Handle(rpc.Context{}, []byte(`<rpc message-id="7" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><close-session/></rpc>`))
	if !strings.Contains(string(out), "<ok/>") {
		t.Errorf("expected ok: %s", out)
	}
}
