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

func editTestDeps(t *testing.T) Deps {
	t.Helper()
	cache := schema.New()
	if err := cache.LoadFiles(filepath.Join("..", "..", "yang", "confd-test.yang")); err != nil {
		t.Fatalf("load: %v", err)
	}
	mock := sysrepoadapter.NewMock(nil)
	tree := &sysrepoadapter.DataNode{XPath: "/", Name: "root", Children: []*sysrepoadapter.DataNode{
		{XPath: "/confd-test:system", Name: "system", NS: "urn:ietf:params:xml:ns:yang:confd-test", Children: []*sysrepoadapter.DataNode{
			{XPath: "/confd-test:system/hostname", Name: "hostname", NS: "urn:ietf:params:xml:ns:yang:confd-test", IsLeaf: true, Value: "old-host"},
		}},
	}}
	mock.SetData(sysrepoadapter.Running, tree)
	mock.SetData(sysrepoadapter.Candidate, &sysrepoadapter.DataNode{Name: "root", XPath: "/"})
	mock.SetData(sysrepoadapter.Startup, &sysrepoadapter.DataNode{Name: "root", XPath: "/"})
	mock.SetData(sysrepoadapter.Operational, tree)
	conn, _ := mock.Connect(context.Background())
	sess, _ := conn.OpenSession(context.Background(), "test")
	return Deps{
		Cache:    cache,
		Conn:     conn,
		Encoder:  data.New(cache),
		Sessions: NewSessionRegistry(),
		Session:  sess,
	}
}

func dispatch(deps Deps, msgID, opName, innerXML string) string {
	d := rpc.NewDispatcher()
	Register(d, deps)
	rctx := rpc.Context{SessionID: 1, PeerUser: "test", Dispatcher: d}
	out := d.Handle(rctx, []byte(`<rpc message-id="`+msgID+`" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">`+innerXML+`</rpc>`))
	return string(out)
}

func TestEditConfig_Merge(t *testing.T) {
	deps := editTestDeps(t)
	reply := dispatch(deps, "1", "edit-config", `<edit-config><target><running/></target><default-operation>merge</default-operation><config><system xmlns="urn:ietf:params:xml:ns:yang:confd-test"><hostname>new-host</hostname></system></config></edit-config>`)
	if !strings.Contains(reply, "<ok/>") {
		t.Fatalf("edit-config merge reply: %s", reply)
	}
	sess := deps.Session
	sess.SwitchDS(sysrepoadapter.Running)
	node, _ := sess.Get(context.Background(), "/")
	if node == nil || len(node.Children) == 0 {
		t.Fatal("running datastore is empty after edit-config")
	}
	sys := node.Children[0]
	if len(sys.Children) == 0 || sys.Children[0].Name != "hostname" || sys.Children[0].Value != "new-host" {
		t.Errorf("hostname not merged: %+v", sys)
	}
}

func TestEditConfig_MissingTarget(t *testing.T) {
	deps := editTestDeps(t)
	reply := dispatch(deps, "2", "edit-config", `<edit-config><config/></edit-config>`)
	if !strings.Contains(reply, "missing-element") {
		t.Errorf("expected missing-element: %s", reply)
	}
}

func TestEditConfig_InvalidDefaultOp(t *testing.T) {
	deps := editTestDeps(t)
	reply := dispatch(deps, "3", "edit-config", `<edit-config><target><running/></target><default-operation>bogus</default-operation><config/></edit-config>`)
	if !strings.Contains(reply, "operation-failed") {
		t.Errorf("expected operation-failed: %s", reply)
	}
}

func TestCommit(t *testing.T) {
	deps := editTestDeps(t)
	reply := dispatch(deps, "4", "edit-config", `<edit-config><target><candidate/></target><default-operation>merge</default-operation><config><system xmlns="urn:ietf:params:xml:ns:yang:confd-test"><hostname>committed-host</hostname></system></config></edit-config>`)
	if !strings.Contains(reply, "<ok/>") {
		t.Fatalf("edit-config on candidate: %s", reply)
	}
	reply = dispatch(deps, "5", "commit", `<commit/>`)
	if !strings.Contains(reply, "<ok/>") {
		t.Fatalf("commit: %s", reply)
	}
	sess := deps.Session
	sess.SwitchDS(sysrepoadapter.Running)
	node, _ := sess.Get(context.Background(), "/")
	sys := node.Children[0]
	if sys.Children[0].Value != "committed-host" {
		t.Errorf("running after commit: %+v", sys.Children[0])
	}
}

func TestDiscardChanges(t *testing.T) {
	deps := editTestDeps(t)
	reply := dispatch(deps, "6", "edit-config", `<edit-config><target><candidate/></target><default-operation>merge</default-operation><config><system xmlns="urn:ietf:params:xml:ns:yang:confd-test"><hostname>discarded</hostname></system></config></edit-config>`)
	if !strings.Contains(reply, "<ok/>") {
		t.Fatalf("edit-config on candidate: %s", reply)
	}
	reply = dispatch(deps, "7", "discard-changes", `<discard-changes/>`)
	if !strings.Contains(reply, "<ok/>") {
		t.Fatalf("discard-changes: %s", reply)
	}
	sess := deps.Session
	sess.SwitchDS(sysrepoadapter.Running)
	node, _ := sess.Get(context.Background(), "/")
	sys := node.Children[0]
	if sys.Children[0].Value != "old-host" {
		t.Errorf("running after discard: %+v", sys.Children[0])
	}
}

func TestCopyConfig_RunningToStartup(t *testing.T) {
	deps := editTestDeps(t)
	sess := deps.Session
	sess.SwitchDS(sysrepoadapter.Startup)
	node, _ := sess.Get(context.Background(), "/")
	if len(node.Children) != 0 {
		t.Fatalf("startup should be empty, got %d children", len(node.Children))
	}
	reply := dispatch(deps, "8", "copy-config", `<copy-config><target><startup/></target><source><running/></source></copy-config>`)
	if !strings.Contains(reply, "<ok/>") {
		t.Fatalf("copy-config: %s", reply)
	}
	sess.SwitchDS(sysrepoadapter.Startup)
	node, _ = sess.Get(context.Background(), "/")
	if len(node.Children) == 0 {
		t.Fatal("startup empty after copy-config")
	}
	sys := node.Children[0]
	if sys.Children[0].Name != "hostname" || sys.Children[0].Value != "old-host" {
		t.Errorf("startup after copy: %+v", sys.Children[0])
	}
}

func TestDeleteConfig_Startup(t *testing.T) {
	deps := editTestDeps(t)
	dispatch(deps, "9", "copy-config", `<copy-config><target><startup/></target><source><running/></source></copy-config>`)
	reply := dispatch(deps, "10", "delete-config", `<delete-config><target><startup/></target></delete-config>`)
	if !strings.Contains(reply, "<ok/>") {
		t.Fatalf("delete-config: %s", reply)
	}
	sess := deps.Session
	sess.SwitchDS(sysrepoadapter.Startup)
	node, _ := sess.Get(context.Background(), "/")
	if len(node.Children) != 0 {
		t.Fatalf("startup should be empty after delete-config, got %d children", len(node.Children))
	}
}

func TestDeleteConfig_Running(t *testing.T) {
	deps := editTestDeps(t)
	reply := dispatch(deps, "11", "delete-config", `<delete-config><target><running/></target></delete-config>`)
	if !strings.Contains(reply, "invalid-value") {
		t.Errorf("expected invalid-value for delete-config on running: %s", reply)
	}
}

func TestValidate_OK(t *testing.T) {
	deps := editTestDeps(t)
	reply := dispatch(deps, "12", "validate", `<validate><source><running/></source></validate>`)
	if !strings.Contains(reply, "<ok/>") {
		t.Errorf("validate: %s", reply)
	}
}
