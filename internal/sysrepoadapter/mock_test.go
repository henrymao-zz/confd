package sysrepoadapter

import (
	"context"
	"testing"
)

func TestMock_GetAndSwitchDS(t *testing.T) {
	m := NewMock(nil)
	tree := &DataNode{XPath: "/", Name: "root", Children: []*DataNode{
		{XPath: "/a", Name: "a", IsLeaf: true, Value: "1"},
	}}
	m.SetData(Running, tree)
	m.SetData(Operational, &DataNode{XPath: "/", Name: "root", Children: []*DataNode{
		{XPath: "/b", Name: "b", IsLeaf: true, Value: "2"},
	}})

	conn, err := m.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	sess, err := conn.OpenSession(context.Background(), "u")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer sess.Close()

	if sess.CurrentDS() != Running {
		t.Errorf("default ds: %v", sess.CurrentDS())
	}
	if err := sess.SwitchDS(Operational); err != nil {
		t.Fatalf("switch: %v", err)
	}
	node, err := sess.Get(context.Background(), "/b")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node == nil || node.Value != "2" {
		t.Errorf("get /b: %+v", node)
	}
}

func TestMock_LockDenial(t *testing.T) {
	m := NewMock(nil)
	conn, _ := m.Connect(context.Background())
	s1, _ := conn.OpenSession(context.Background(), "u1")
	if err := s1.Lock(Running); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	s2, _ := conn.OpenSession(context.Background(), "u2")
	if err := s2.Lock(Running); err == nil {
		t.Error("expected lock-denied on second lock")
	}
	if err := s1.Unlock(Running); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := s2.Lock(Running); err != nil {
		t.Errorf("lock after unlock: %v", err)
	}
}

func TestDatastoreByName(t *testing.T) {
	cases := []struct {
		in   string
		want Datastore
		ok   bool
	}{
		{"running", Running, true},
		{"startup", Startup, true},
		{"candidate", Candidate, true},
		{"operational", Operational, true},
		{"", Operational, true},
		{"bogus", 0, false},
	}
	for _, c := range cases {
		got, err := DatastoreByName(c.in)
		if c.ok {
			if err != nil || got != c.want {
				t.Errorf("DatastoreByName(%q) = %v,%v want %v,nil", c.in, got, err, c.want)
			}
		} else if err == nil {
			t.Errorf("DatastoreByName(%q) expected error", c.in)
		}
	}
}
