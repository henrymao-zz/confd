# Proposal: Replace Hand-Rolled Packages with Open-Source Go Modules

> Status: **proposal** — not yet implemented. This document scopes the
> replacement of `internal/framing`, `internal/transport`, `internal/data`,
> and parts of `internal/rpc`/`internal/hello` with the well-maintained
> Go NETCONF library **[nemith.io/netconf](https://github.com/nemith/netconf)**.

## 1. Why replace

The four hand-rolled packages total ~1,200 LoC and re-implement
RFC 6241/6242 protocol mechanics that already exist in tested, maintained
Go libraries:

| Package | LoC | What it does | Why replace |
|---|---|---|---|
| `internal/framing` | ~190 | RFC 6242 chunked + EOM framing | reimplements `transport.Framer` |
| `internal/transport` | ~180 | SSH listener + channel handling | reimplements SSH transport; missing SSH server transport upstream |
| `internal/hello` | ~90 | `<hello>` build/parse + capability negotiation | reimplements `netconf.Hello` + `CapabilitySet` |
| `internal/data` | ~100 | `DataNode` → NETCONF XML encoder | no OSS replacement; **keep** |
| `internal/rpc` | ~260 | `<rpc>` parse, dispatch, `<rpc-error>` | reimplements `netconf.RPC`/`Reply`/`RPCError`/`ErrTag` |

## 2. The library: `nemith.io/netconf`

**`github.com/nemith/netconf`** (module path `nemith.io/netconf`) is the
de-facto successor to the deprecated `github.com/Juniper/go-netconf`,
actively maintained by the original author, BSD-2-Clause licensed.

### What it provides that we need

| Our package | nemith equivalent | Notes |
|---|---|---|
| `framing.Reader`/`Writer` + `Mode` + `Upgrade()` | `transport.Framer` (`NewFramer(r, w)`, `.Upgrade()`, `.MsgReader()`, `.MsgWriter()`) | Drop-in: same EOM→chunked upgrade flow, same `io.Reader`/`io.Writer` model |
| `transport.Transport` interface + `sshTransport` + `Pipe` | `transport.Transport` interface (`MsgReader`/`MsgWriter`/`Close`) + `transport/ssh.Transport` (client) | nemith ships a **client** SSH transport; we need a **server** SSH transport — see §4 |
| `hello.Build`/`Parse`/`Negotiate` | `netconf.Hello` struct + `CapabilitySet` + `DefaultCapabilities` + `ExpandCapability` | Covers hello build/parse, capability set membership, base:1.0/1.1 negotiation |
| `rpc.Message`/`ParseMessage`/`Reply`/`Encode`/`Error`/`ErrorTag`/`Handler`/`Dispatcher` | `netconf.RPC` / `Reply` / `RPCError` / `RPCErrors` / `ErrTag` / `ErrSeverity` / `ErrType` | Full RFC 6241 §4.8 error taxonomy, typed operation structs in `rpc/` |
| `operations.Deps` + per-op handlers | `rpc.GetConfigReq`, `rpc.EditConfigReq`, `rpc.Lock`, `rpc.Get`, `rpc.KillSession`, etc. | Typed request/reply structs for all 13 base operations |

### What it does NOT provide

1. **Server-side SSH transport** — nemith's `transport/ssh.Transport` is a
   *client* (`Dial`). There is no `Listen`/`Accept`. We write a thin
   server-side adapter (§4).
2. **Server-side session loop** — nemith's `Session` is a client that
   sends RPCs and waits for replies. A server receives RPCs and sends
   replies. We write a thin server loop (§5).
3. **YANG data → NETCONF XML encoding** — no Go library does this.
   `internal/data` stays hand-rolled (§6).
4. **YANG schema cache** — `internal/schema` (goyang) is unrelated to
   NETCONF protocol; stays unchanged.

## 3. Package mapping

```
BEFORE                               AFTER
────────────────────────────────────────────────────────────────
internal/framing/         ──▶  nemith.io/netconf/transport (Framer)
internal/transport/       ──▶  nemith.io/netconf/transport (Transport)
                               + internal/nettrans/sshserver.go (thin)
internal/hello/           ──▶  nemith.io/netconf (Hello, CapabilitySet)
internal/rpc/             ──▶  nemith.io/netconf (RPC, Reply, RPCError)
                               + internal/nettrans/dispatch.go (thin)
internal/data/            ──▶  KEEP (no OSS replacement)
internal/schema/          ──▶  KEEP (goyang, unchanged)
internal/operations/      ──▶  KEEP (handlers, now using nemith types)
internal/pluginhost/      ──▶  KEEP (unchanged)
internal/sysrepoadapter/  ──▶  KEEP (unchanged)
internal/server/          ──▶  KEEP (rewired to use new transport/dispatch)
```

New package: **`internal/nettrans`** — a thin (~150 LoC) adapter layer
that wraps nemith's `transport.Transport` for server-side SSH and provides
the server-side RPC dispatch loop that nemith doesn't ship.

## 4. Server-side SSH transport

nemith's `transport.Transport` interface is:
```go
type Transport interface {
    MsgReader() (io.ReadCloser, error)  // next framed message
    MsgWriter() (io.WriteCloser, error) // new framed message
    Close() error
}
```

We implement this for the **server side** of an SSH connection by wrapping
`transport.Framer` around an `ssh.Channel`:

```go
// internal/nettrans/sshserver.go
package nettrans

import (
    "io"
    "nemith.io/netconf/transport"
    "golang.org/x/crypto/ssh"
)

// SSHServerTransport implements transport.Transport for the server side
// of a NETCONF-over-SSH session. It wraps an ssh.Channel with a Framer.
type SSHServerTransport struct {
    conn  *ssh.ServerConn
    ch    ssh.Channel
    framer *transport.Framer
}

func NewSSHServerTransport(conn *ssh.ServerConn, ch ssh.Channel) *SSHServerTransport {
    return &SSHServerTransport{
        conn:   conn,
        ch:     ch,
        framer: transport.NewFramer(ch, ch),
    }
}

func (t *SSHServerTransport) MsgReader() (io.ReadCloser, error) {
    return t.framer.MsgReader()
}

func (t *SSHServerTransport) MsgWriter() (io.WriteCloser, error) {
    return t.framer.MsgWriter()
}

func (t *SSHServerTransport) Upgrade() { t.framer.Upgrade() }

func (t *SSHServerTransport) Close() error {
    _ = t.ch.Close()
    return t.conn.Close()
}

func (t *SSHServerTransport) PeerUser() string { return t.conn.User() }
```

The SSH listener (accepting connections + subsystem handling) stays in
`internal/server` but is simplified — it no longer manages framing, just
SSH accept + channel + subsystem request, then hands the channel to
`nettrans.NewSSHServerTransport`.

## 5. Server-side RPC dispatch

nemith's `Session` is client-oriented (send RPC, await reply by
message-id). A server does the inverse: receive RPC, dispatch by
operation name, send reply. We write a thin server loop:

```go
// internal/nettrans/dispatch.go
package nettrans

import (
    "encoding/xml"
    "io"
    "nemith.io/netconf"
)

// ServerHandler handles a single RPC and returns a reply body.
type ServerHandler func(rpc *netconf.RPC) (reply any, err error)

// ServerLoop reads framed messages from transport, decodes <rpc>,
// dispatches to handlers, and writes <rpc-reply>.
func ServerLoop(
    tr transport.Transport,
    handlers map[string]ServerHandler,
    serverCaps netconf.CapabilitySet,
    sessionID uint64,
) error {
    // 1. Send <hello> with serverCaps + sessionID
    // 2. Read peer <hello>, negotiate base:1.1 → tr.Upgrade()
    // 3. Loop: read message → decode <rpc> → dispatch → write <rpc-reply>
    // 4. On <close-session> or EOF → return
}
```

The `ServerLoop` replaces `server.ServeTransport`'s hello + rpc loop.
Operation handlers in `internal/operations/` switch from our `rpc.Handler`
interface to `nettrans.ServerHandler`, using nemith's typed request structs
(`rpc.GetConfigReq`, etc.) for parsing and `netconf.RPCError` for errors.

## 6. What stays hand-rolled

| Package | Why it stays |
|---|---|
| `internal/data` | No Go library encodes a YANG data tree → NETCONF XML. `ydk-go` is archived + CGO. We keep the `DataNode` → XML encoder. |
| `internal/schema` | goyang is the YANG schema parser; unrelated to NETCONF protocol. |
| `internal/operations/` | Handlers are confd-specific (call sysrepoadapter, use schema cache). They adopt nemith types for I/O but the logic stays. |
| `internal/sysrepoadapter/` | sysrepo cgo bindings; unrelated to NETCONF protocol. |
| `internal/pluginhost/` | dlopen plugin host; unrelated to NETCONF protocol. |

## 7. Migration impact

| Metric | Before | After |
|---|---|---|
| Hand-rolled NETCONF protocol LoC | ~720 (framing + transport + hello + rpc) | ~150 (nettrans: sshserver + dispatch) |
| Dependencies | goyang, x/crypto | + `nemith.io/netconf` |
| Tests | 12 packages, all pass | Tests restructured; nettrans unit-tested; existing server tests use nemith transport |
| Risk | — | nemith is well-maintained (BSD-2), but we're the first server-side user; the `transport.Framer` is the most battle-tested piece |

## 8. Phasing

| Phase | Scope | Exit criteria |
|---|---|---|
| **F1** | Add `nemith.io/netconf` dependency; implement `nettrans.SSHServerTransport`; replace `internal/framing` with `transport.Framer` | Existing server tests pass using nemith framing |
| **F2** | Replace `internal/hello` with nemith `Hello` + `CapabilitySet` | Capability negotiation tests pass |
| **F3** | Replace `internal/rpc` with nemith `RPC`/`Reply`/`RPCError` + `nettrans.ServerLoop` | All operation tests pass |
| **F4** | Remove `internal/framing`, `internal/hello`, `internal/rpc` | Dead code eliminated; build + tests green |
| **F5** | Replace `internal/transport` SSH listener with simplified version using `nettrans` | SSH end-to-end test passes |

## 9. Risks

| Risk | Mitigation |
|---|---|
| nemith is client-oriented; server-side use is novel | The `transport.Framer` and `Hello`/`RPC`/`RPCError` types are protocol-level, not client-specific. The server loop is our code, not nemith's. |
| nemith requires Go 1.25+ (go.mod says 1.25.0) | We already use Go 1.26; no conflict. |
| nemith API may change (v0.0.x) | Pin a specific version in go.mod; vendor if needed. |
| `internal/data` encoder is a gap in the ecosystem | Accept it; the encoder is small and well-tested. Phase 2 could generate it from goyang. |
| nemith `transport.Transport` interface may diverge from our needs | It's a simple 3-method interface; if it changes, the adapter absorbs the diff. |

## 10. Summary

Replace `internal/framing`, `internal/hello`, `internal/rpc`, and most of
`internal/transport` with **`nemith.io/netconf`** — the actively-maintained
successor to Juniper/go-netconf. Add a thin `internal/nettrans` adapter
(~150 LoC) for the server-side SSH transport and RPC dispatch loop that
nemith doesn't ship. Keep `internal/data` (YANG→XML encoding) and
`internal/schema` (goyang) hand-rolled — no Go library covers those.

Net result: ~570 lines of hand-rolled NETCONF protocol code replaced with
a tested, maintained library; confd's codebase shrinks to its
differentiating layers (sysrepo adapter, plugin host, YANG schema cache,
data encoder, operation handlers).
