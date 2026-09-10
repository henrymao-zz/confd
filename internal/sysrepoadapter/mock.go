package sysrepoadapter

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Mock is an in-memory Adapter backed by a simple YANG-modeled tree.
// It is safe for concurrent use. It exists so confd can run end-to-end
// tests (and serve as a reference) without a real sysrepod.
type Mock struct {
	mu      sync.Mutex
	trees   map[Datastore]*DataNode
	modules []ModuleInfo
	locks   map[Datastore]bool
}

// NewMock returns a Mock seeded with the given modules and an empty
// data tree for each datastore.
func NewMock(modules []ModuleInfo) *Mock {
	m := &Mock{
		trees:   map[Datastore]*DataNode{},
		modules: modules,
		locks:   map[Datastore]bool{},
	}
	for _, ds := range []Datastore{Running, Startup, Candidate, Operational} {
		m.trees[ds] = &DataNode{Name: "root", XPath: "/"}
	}
	return m
}

// SetData replaces the tree for a datastore. Useful for test setup.
func (m *Mock) SetData(ds Datastore, root *DataNode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if root == nil {
		root = &DataNode{Name: "root", XPath: "/"}
	}
	m.trees[ds] = root
}

// Connect satisfies Adapter.
func (m *Mock) Connect(ctx context.Context) (Conn, error) {
	return &mockConn{mock: m}, nil
}

type mockConn struct {
	mock *Mock
}

func (c *mockConn) ListModules(ctx context.Context) ([]ModuleInfo, error) {
	c.mock.mu.Lock()
	defer c.mock.mu.Unlock()
	out := make([]ModuleInfo, len(c.mock.modules))
	copy(out, c.mock.modules)
	return out, nil
}

func (c *mockConn) OpenSession(ctx context.Context, user string) (Session, error) {
	return &mockSession{mock: c.mock, ds: Running}, nil
}

func (c *mockConn) Close() error { return nil }

type mockSession struct {
	mock *Mock
	mu   sync.Mutex
	ds   Datastore
}

func (s *mockSession) SwitchDS(ds Datastore) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ds = ds
	return nil
}

func (s *mockSession) CurrentDS() Datastore {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ds
}

func (s *mockSession) Get(ctx context.Context, xpath string) (*DataNode, error) {
	s.mock.mu.Lock()
	defer s.mock.mu.Unlock()
	root := s.mock.trees[s.ds]
	if root == nil {
		return nil, ErrNotFound
	}
	if xpath == "" || xpath == "/" {
		return root, nil
	}
	node := findInTree(root, xpath)
	if node == nil {
		return nil, ErrNotFound
	}
	return node, nil
}

func (s *mockSession) Lock(ds Datastore) error {
	s.mock.mu.Lock()
	defer s.mock.mu.Unlock()
	if s.mock.locks[ds] {
		return fmt.Errorf("sysrepo: %s already locked", ds)
	}
	s.mock.locks[ds] = true
	return nil
}

func (s *mockSession) Unlock(ds Datastore) error {
	s.mock.mu.Lock()
	defer s.mock.mu.Unlock()
	s.mock.locks[ds] = false
	return nil
}

func (s *mockSession) Close() error { return nil }

// findInTree walks a DataNode tree looking for a node whose XPath ends with
// the given path (or starts with it). xpaths in the mock are of the form
// "/module/container/list[key='v']/leaf".
func findInTree(root *DataNode, xpath string) *DataNode {
	xpath = strings.TrimSuffix(xpath, "/")
	if xpath == "" {
		return root
	}
	var walk func(n *DataNode) *DataNode
	walk = func(n *DataNode) *DataNode {
		if n.XPath == xpath {
			return n
		}
		for _, c := range n.Children {
			if r := walk(c); r != nil {
				return r
			}
		}
		return nil
	}
	return walk(root)
}
