// Package sysrepoadapter defines the Go-native interface confd uses to talk
// to a sysrepo-backed datastore. It is deliberately cgo-free so the rest of
// confd can be built and tested without libsysrepo installed.
//
// Two implementations are provided:
//   - Mock: an in-memory YANG-modeled tree used for tests and as the
//     default when libsysrepo is not available.
//   - CGo: a thin cgo wrapper around libsysrepo, selected by the `sysrepo`
//     build tag.
package sysrepoadapter

import (
	"context"
	"errors"
	"unsafe"
)

// Datastore enumerates the sysrepo datastores confd can address.
type Datastore int

const (
	Running Datastore = iota
	Startup
	Candidate
	Operational
)

// String returns the NETCONF <source> name for a datastore.
func (d Datastore) String() string {
	switch d {
	case Running:
		return "running"
	case Startup:
		return "startup"
	case Candidate:
		return "candidate"
	case Operational:
		return "operational"
	}
	return "unknown"
}

// DatastoreByName parses a NETCONF <source> value ("running", "startup",
// "candidate", or "" for operational <get>) into a Datastore.
func DatastoreByName(name string) (Datastore, error) {
	switch name {
	case "running", "":
		if name == "" {
			return Operational, nil
		}
		return Running, nil
	case "startup":
		return Startup, nil
	case "candidate":
		return Candidate, nil
	case "operational":
		return Operational, nil
	}
	return 0, errors.New("unknown datastore: " + name)
}

// DataNode is the Go-native projection of a libyang data tree node. It is a
// tree: leaves carry a Value, containers/lists carry Children.
type DataNode struct {
	XPath    string      // canonical XPath of this node
	Name     string      // local element name
	NS       string      // namespace URI
	Value    string      // leaf/leaf-list value (empty for containers/lists)
	IsLeaf   bool
	IsLeafList bool
	IsList   bool
	Key      string      // for list entries, the key leaf value (for ordering)
	Children []*DataNode
}

// ModuleInfo describes a YANG module known to sysrepo (for <get-schema> and
// capability advertisement when sysrepo is the source of truth).
type ModuleInfo struct {
	Name       string
	Revision   string
	Namespace  string
	SourcePath string // on-disk .yang path (for goyang to load)
}

// Adapter is the top-level factory for connections.
type Adapter interface {
	Connect(ctx context.Context) (Conn, error)
}

// Conn is a long-lived connection to the datastore daemon.
type Conn interface {
	ListModules(ctx context.Context) ([]ModuleInfo, error)
	OpenSession(ctx context.Context, user string) (Session, error)
	Close() error
}

// Session is a per-NETCONF-session datastore handle.
type Session interface {
	SwitchDS(ds Datastore) error
	CurrentDS() Datastore
	// Get returns the subtree rooted at xpath, or a flat list of matching
	// nodes if xpath selects multiple siblings. An empty xpath ("") or "/"
	// selects the whole datastore root.
	Get(ctx context.Context, xpath string) (*DataNode, error)
	Lock(ds Datastore) error
	Unlock(ds Datastore) error

	// --- edit operations (phase 2) ---

	// EditBatch loads a parsed edit tree into the session's staging area.
	// defaultOp is "merge", "replace", or "none" (RFC 6241 §7.2).
	EditBatch(edit *DataNode, defaultOp string) error

	// ApplyChanges commits the staged edits to the current datastore.
	ApplyChanges(timeoutMs uint32) error

	// DiscardChanges discards all staged edits.
	DiscardChanges() error

	// Validate validates the current datastore + staged edits without
	// applying them.
	Validate(moduleName string, timeoutMs uint32) error

	// CopyConfig replaces the current session's datastore with the
	// contents of srcDatastore.
	CopyConfig(moduleName string, srcDatastore Datastore, timeoutMs uint32) error

	// ReplaceConfig replaces the current session's datastore with the
	// given config tree. If config is nil, the datastore is cleared
	// (used by <delete-config>).
	ReplaceConfig(moduleName string, config *DataNode, timeoutMs uint32) error

	Close() error
}

// ErrNotFound is returned when an xpath selects no nodes.
var ErrNotFound = errors.New("sysrepo: no data at xpath")

// RawConnProvider is optionally implemented by cgo-based Conn
// implementations to expose the raw sr_conn_ctx_t* so the plugin host
// can create per-plugin sessions on the same connection.
type RawConnProvider interface {
	RawConn() unsafe.Pointer
}
