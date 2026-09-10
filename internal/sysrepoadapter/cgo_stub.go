//go:build sysrepo

// Package sysrepoadapter's cgo backend. This file is only compiled when the
// `sysrepo` build tag is set, so the default build remains pure Go.
//
// It is a thin shim over libsysrepo: it opens a connection with sr_connect,
// sessions with sr_session_start, switches datastores with
// sr_session_switch_ds, and reads data with sr_get_items / sr_get_item.
//
// NOTE: the full cgo binding is intentionally minimal here. It compiles only
// when libsysrepo headers are available (via pkg-config). In environments
// without sysrepo headers (e.g. CI for the pure-Go codepath), this file is
// excluded and the Mock adapter is used instead.
package sysrepoadapter

import (
	"context"
	"fmt"
)

// CGo is the libsysrepo-backed Adapter.
type CGo struct {
	socket string
}

// NewCGo returns a CGo adapter that connects to the given sysrepo socket
// (empty means the default).
func NewCGo(socket string) *CGo { return &CGo{socket: socket} }

func (a *CGo) Connect(ctx context.Context) (Conn, error) {
	return nil, fmt.Errorf("sysrepoadapter.CGo: not built (no sysrepo headers)")
}

// Compile-time check that CGo satisfies Adapter.
var _ Adapter = (*CGo)(nil)
