//go:build !sysrepo

// When the `sysrepo` build tag is NOT set, provide a stub NewCGo so that
// cmd/confd can reference it unconditionally. The stub returns an error on
// Connect, directing users to either install sysrepo headers or use the
// mock backend.
package sysrepoadapter

import (
	"context"
	"errors"
)

// CGo is the libsysrepo-backed Adapter. Without the `sysrepo` build tag it
// is a non-functional placeholder.
type CGo struct {
	socket string
}

// NewCGo returns a CGo adapter.
func NewCGo(socket string) *CGo { return &CGo{socket: socket} }

func (a *CGo) Connect(ctx context.Context) (Conn, error) {
	return nil, errors.New("sysrepoadapter: cgo backend not built (rebuild with -tags sysrepo and libsysrepo headers installed)")
}

var _ Adapter = (*CGo)(nil)
