# confd — Go-based NETCONF Server Design Proposal

> A Go implementation of a NETCONF server (`confd`) intended to be a lighter,
> easier-to-extend alternative to Netopeer2. It reuses **sysrepo** as the
> datastore back-end and **goyang** (openconfig/goyang) as the YANG schema
> parser. Initial scope: read-only access to configuration and operational
> data via NETCONF (RFC 6241), with edit/commit/NACM added in later phases.

---

## 1. Goals & Non-Goals

### 1.1 Goals (MVP)
- Listen on SSH (RFC 6242) for NETCONF 1.1 sessions.
- Implement the NETCONF base protocol operations needed to **retrieve**
  configuration and state data:
  - `<get>`, `<get-config>` (with `--source` running/startup/candidate)
  - `<get>` with `<filter>` (subtree + XPath)
  - `<lock>`, `<unlock>`, `<close-session>`, `<kill-session>` (housekeeping)
  - `<get-schema>` (RFC 6022) — cheap once goyang is wired in.
- Expose data stored in **sysrepo** (running + operational datastores).
- Validate GET reply payloads against the YANG schema parsed by **goyang**.
- Produce standards-compliant `<ok/>` / `<rpc-error>` responses.

### 1.2 Non-Goals (for MVP)
- `<edit-config>` / `<copy-config>` / `<delete-config>` / `<commit>` —
  deferred to phase 2 (they require full NACM + transaction plumbing).
- Notifications / `<create-subscription>` — phase 3.
- Call-home (RFC 8071), TLS transport (RFC 7589) — phase 4.
- RESTCONF — separate project, out of scope here.

### 1.3 Phase 2+ Roadmap (sketch)
1. `<edit-config>` + transactions (`<lock>`, `<commit>`, `<discard-changes>`).
2. NACM (RFC 6536) enforcement on edit operations.
3. `<notification>` replay via sysrepo notification store.
4. `<validate>`, `<action>` (YANG 1.1 RPC/action) dispatch.
5. TLS / call-home / clustered multi-tenant operation.

---

## 2. High-Level Architecture

```
              ┌────────────────────────────────────────────────────┐
              │                     confd                          │
              │                                                    │
   SSH client  │  ┌────────────┐   ┌──────────────┐   ┌──────────┐ │
   ────────────┼─▶│  transport │──▶│   netconf    │──▶│  rpc     │ │
   (RFC 6242)  │  │  ssh/tcp   │   │  framing +   │   │ dispatch │ │
              │  │            │   │  <hello>     │   │          │ │
              │  └────────────┘   └──────────────┘   └────┬─────┘ │
              │                                          │       │
              │         ┌────────────────┐  ┌────────────▼─────┐ │
              │         │     goyang      │  │   operations     │ │
              │         │  schema cache   │◀─│ get/get-config/  │ │
              │         │  (Entry trees)  │  │ get-schema/...   │ │
              │         └────────┬───────┘  └─────────┬────────┘ │
              │                  │                    │          │
              │         ┌────────▼────────────────────▼────────┐ │
              │         │             sysrepo adapter           │ │
              │         │   sr_connect / sr_session_* /         │ │
              │         │   sr_get_items / sr_get_item         │ │
              │         │   (cgo bindings, per-session ctx)    │ │
              │         └────────────────────┬─────────────────┘ │
              └──────────────────────────────┼───────────────────┘
                                             │  libsysrepo.so
                                             ▼
                                    ┌─────────────────┐
                                    │     sysrepod     │
                                    │  (datastores)   │
                                    └─────────────────┘
```

### 2.1 Process model
- **Single binary `confd`** with sub-commands:
  - `confd serve`        — start the NETCONF listener.
  - `confd schema list`  — dump loaded YANG modules (debug aid).
  - `confd rpc ...`      — local RPC client for integration tests.
- A long-lived listener accepts SSH connections. Each connection is a
  goroutine. **One sysrepo connection per NETCONF session** is opened on
  demand (sysrepo supports multiple concurrent connections/sessions).
- Schema parsing with goyang is done **once at startup** and the resolved
  `*yang.Entry` trees are cached in a `SchemaCache` (concurrent-read-safe).
  The schema cache is the source of truth for `get-schema`, for building
  the `<hello>` capability list, and for validating GET filter paths.

### 2.2 Why goyang + sysrepo (and the impedance mismatch)

| Concern                | goyang                                | sysrepo/libyang                      |
|------------------------|---------------------------------------|--------------------------------------|
| In-memory model        | `*yang.Entry` AST                     | `struct lyd_node` data trees         |
| XPath / filter eval    | manual walk of Entry tree             | native libyang XPath                 |
| Validation             | structural, not data-instance aware   | full data validation                 |
| Encoding               | Go structs / ygot                    | JSON, XML, lyb (binary)              |

We treat them as **complementary, not redundant**:

- **goyang** is used at the schema/control plane: capability advertisement,
  `get-schema`, NACM rule resolution (phase 2), filter path parsing, and
  generating Go-side typed views for tests.
- **sysrepo/libyang** is used at the **data plane**: actual data values come
  from sysrepo already typed as `lyd_node`, already validated, already in the
  correct datastore. The adapter converts `lyd_node` trees directly to the
  NETCONF XML reply using libyang's own XML encoder — *we never re-encode
  through goyang for the data path*. This avoids the trap of having two
  competing in-memory representations of the data and keeps the MVP small.

The consequence: **goyang is consulted for "what does the schema look like",
libyang is consulted for "what does the data look like"**. Filter processing
splits along the same line — XPath/subtree filters are translated into
sysrepo/libyang-style filters and the result is returned by libyang, while
goyang is used to decide whether a filter refers to a valid path/capability.

---

## 3. Component Breakdown

### 3.1 `transport` — NETCONF over SSH
- Wraps `golang.org/x/crypto/ssh` for the SSH channel.
- Implements RFC 6242 framing: EOM marker `[<chunk>...` framing, plus the
  base-1.0 `\n]]>]]>\n` fallback during `<hello>`.
- Configurable: host key path, authorized-keys file, bind address, port
  (default 830), `max-sessions`, `idle-timeout`.
- Optional debug transport (`netconf+tcp://`) for CI without SSH overhead.
- Interface:
  ```go
  type Transport interface {
      Send(msg []byte) error               // frames + writes
      Recv() ([]byte, error)               // un-frames, returns full rpc
      Close() error
      PeerUser() string                    // for NACM / audit
  }
  ```

### 3.2 `framing` — RFC 6242 message codec
- Pure-Go chunked framing parser. Knows how to upgrade from 1.0 to 1.1 once
  both sides advertise the `:base:1.1` capability.
- Exposes `Reader` / `Writer` that operate on top of the SSH channel's
  `io.Reader`/`io.Writer`.

### 3.3 `hello` — capability negotiation
- On connect, send `<hello>` with:
  - `:base:1.1` (and `:base:1.0` for back-compat).
  - one URI per YANG module in the schema cache (module + revision + features).
  - `:with-defaults` (RFC 6131), `:validate:1.1`, `:validate:1.0` — only if
    actually implemented; we do *not* advertise capabilities we don't serve.
- Parse peer `<hello>`, record the agreed capabilities, and switch the
  framing to 1.1 when both sides support it.

### 3.4 `rpc` — message routing
- Decodes `<rpc message-id="..."> ... </rpc>`.
- Looks up the inner operation QName against a `map[string]Handler`.
- Each `Handler` returns either an `<ok/>`, a data tree reply, or an
  `<rpc-error>` with the right `error-tag`/`error-severity`/`error-info`.
- Enforces a per-session `<lock>`-aware critical section so that concurrent
  RPCs from the same session are serialized (NETCONF requires this).
- All errors follow RFC 6241 Appendix A error taxonomy, reusing the
  translation helpers that sysrepo already exposes via
  `sr_error_format()`.

### 3.5 `operations` — protocol operation handlers
MVP handlers:

| Operation            | Source            | Notes                                                  |
|----------------------|-------------------|--------------------------------------------------------|
| `get-config`         | sysrepo running/  | `sr_session_switch_ds(SR_DS_RUNNING/STARTUP/...)`.     |
|                      | startup/candidate | libyang serializes the resulting `lyd_node` to XML.    |
| `get`                | sysrepo oper      | use `sr_session_switch_ds(SR_DS_OPERATIONAL)`.        |
| `get-schema`         | goyang cache      | returns module source/text/revision; RFC 6022.        |
| `lock`/`unlock`      | sysrepo lock API  | per-datastore lock.                                    |
| `close-session`      | local             | tears down SSH session.                                |
| `kill-session`       | local             | kills a named session-id.                              |

Each handler is a single file in `internal/operations/<op>.go` implementing:
```go
type Handler interface {
    Handle(ctx context.Context, s *Session, r *RpcRequest) (*RpcReply, error)
}
```

#### 3.5.1 Filter handling
- **Subtree filter (RFC 6241 §6.4)**: convert to a libyang filter via
  `lyd_new_path` + select, or fall back to a Go-side walker when libyang
  cannot express the merge semantics. The MVP implements "Containment" and
  "Selection nodes" exactly as in the RFC; `content-match` nodes are
  supported for the common `leaf`/`leaf-list` case.
- **XPath filter (`type="xpath"`)**: pass the XPath directly to
  `sr_get_items` / `sr_get_items_iter` with the XPath; libyang does the rest.
- Any filter path is first validated against the goyang schema cache so an
  unknown path yields `<rpc-error error-tag="unknown-element">`.

### 3.6 `schema` — goyang cache
```go
type Cache struct {
    modules map[string]*yang.Module   // by "module@rev"
    entries  map[string]*yang.Entry    // top-level entries per module
    caps     []string                  // capability URIs for <hello>
    mu       sync.RWMutex
}
```
- Loads every module installed in sysrepo. We get the module list from
  sysrepo itself (`sr_get_module_list`) — *sysrepo remains the source of
  truth for which modules are installed*, so `confd` and sysrepod never
  disagree.
- For each module we ask sysrepo for the on-disk `.yang` path and feed it
  into `yang.Parse` with the right include/import search paths.
- Caches:
  - The `*yang.Entry` tree (resolved augments, uses, groupings).
  - The list of features (with their conformance state).
  - The original YANG text — used by `<get-schema>` and by tests.
- The cache is rebuilt on SIGHUP and on a sysrepo "module installed"
  notification (phase 2).

### 3.7 `sysrepoadapter` — cgo bindings to libsysrepo
This is the single most awkward component because **there is no official Go
binding for sysrepo**. Options, in order of preference:

1. **CGo bindings written in-repo.** Thin `//export` wrappers in
   `internal/sysrepoc/cgo.go` around the small surface we need:
   `sr_connect`, `sr_session_start_*`, `sr_session_switch_ds`,
   `sr_get_item`, `sr_get_items`, `sr_get_items_iter`, `sr_get_item_next`,
   `sr_lock`, `sr_unlock`, `sr_session_stop`, `sr_disconnect`,
   `sr_get_module_list`, `sr_get_module_info`, plus the `sr_error_*`
   helpers. Wrappers return Go errors via `errors.New(sr_get_error(...))`.
2. **Reuse sysrepo-cpp via a small C++ shim** — rejected: pulls in a C++
   runtime and complicates the build.
3. **Pure-Go reimplementation of the sysrepo IPC protocol** — rejected for
   MVP; too brittle against upstream changes.

The adapter exposes a Go-native interface that hides `lyd_node` and
`sr_session_*`:

```go
type Adapter interface {
    Connect() (Conn, error)
}

type Conn interface {
    ListModules(ctx context.Context) ([]ModuleInfo, error)
    ModulePath(ctx context.Context, name string) (string, error)   // for goyang
    OpenSession(ctx context.Context, user string) (Session, error)
}

type Session interface {
    SwitchDS(ds Datastore) error
    GetItems(ctx context.Context, xpath string) ([]DataNode, error)
    GetItem (ctx context.Context, xpath string) (*DataNode, error)
    Lock  (ctx context.Context, ds Datastore) error
    Unlock(ctx context.Context, ds Datastore) error
    Close() error
}

type DataNode struct {
    XPath string
    Value string
    Type  yang.TypeKind
    // ... leaf-list / children helpers
}
```

`DataNode` is a thin Go value object; the underlying `lyd_node*` is owned by
the adapter for the lifetime of the call and freed before returning. For the
MVP we return Go-side parsed values; in phase 2 we'll let the operations
layer request a `lyd_node`-backed tree directly when XML serialization needs
it (so we don't double-encode through Go).

The adapter also implements the **error → `<rpc-error>` mapping** by walking
`sr_error_format` output and emitting the matching `error-tag`/`error-info`.

### 3.8 `config` — runtime configuration
A YAML/TOML file (`/etc/confd/confd.yaml`) plus CLI flags:
```yaml
listen:
  ssh:
    bind: 0.0.0.0:830
    host_key: /etc/confd/host_key
    authorized_keys: /etc/confd/authorized_keys
    idle_timeout: 30m
  tcp:                       # optional, CI only
    enabled: false
    bind: 127.0.0.1:1830
sysrepo:
  socket: ""                 # default sysrepo socket
  datastore_default: running
schema:
  extra_paths: []            # extra YANG search dirs for goyang
log:
  level: info
  format: json
```

### 3.9 `logging`
- `log/slog` structured logger.
- Per-session context fields: `session_id`, `peer_user`, `peer_addr`,
  `message_id`, `operation`.
- An opt-in raw-RPC dump (gated behind `log.level=debug`) for development.

---

## 4. Directory Layout

```
confd/
├── cmd/
│   └── confd/
│       ├── main.go              # cobra root + `serve` subcommand
│       └── serve.go
├── internal/
│   ├── config/                 # YAML + flag parsing
│   ├── transport/
│   │   ├── ssh.go              # RFC 6242 SSH transport
│   │   ├── tcp.go              # plaintext transport (CI/debug)
│   │   └── transport.go        # Transport interface
│   ├── framing/
│   │   └── framing.go          # RFC 6242 chunked + 1.0 fallback
│   ├── hello/
│   │   ├── hello.go            # capability negotiation
│   │   └── capabilities.go     # capability builders
│   ├── rpc/
│   │   ├── rpc.go              # <rpc> parse, message-id, dispatch
│   │   ├── error.go            # <rpc-error> builders per RFC 6241 App A
│   │   └── handler.go          # Handler interface + registry
│   ├── operations/
│   │   ├── get.go
│   │   ├── getconfig.go
│   │   ├── getschema.go
│   │   ├── lock.go
│   │   ├── unlock.go
│   │   ├── close_session.go
│   │   ├── kill_session.go
│   │   └── filter/             # subtree + xpath filter processing
│   │       ├── subtree.go
│   │       └── xpath.go
│   ├── schema/
│   │   ├── cache.go            # goyang-backed SchemaCache
│   │   ├── loader.go           # discovers modules from sysrepo
│   │   └── getschema.go        # RFC 6022 <get-schema> helpers
│   ├── sysrepoadapter/         # public Go interface
│   │   ├── adapter.go          # Adapter, Conn, Session
│   │   ├── types.go            # DataNode, Datastore, ModuleInfo
│   │   └── errors.go           # sysrepo → rpc-error mapping
│   ├── sysrepoc/               # cgo layer (only file that knows libsysrepo)
│   │   ├── cgo.go              # //cgo pkg-config: sysrepo
│   │   ├── session.go
│   │   ├── items.go
│   │   ├── modules.go
│   │   └── error.go
│   ├── session/               # NETCONF session state
│   │   └── session.go         # session-id, peer info, locks, ds
│   └── server/
│       └── server.go          # ties transport + rpc + adapter together
├── pkg/                        # public (no internal), for tooling/tests
│   └── netconf/               # message types if ever exposed
├── yang/                       # in-tree YANG modules used by tests
├── test/
│   ├── integration/           # docker-compose with sysrepod + confd
│   └── regression/            # captured rpc traces from Netopeer2
├── go.mod
├── go.sum
├── Makefile
├── README.md
└── .github/workflows/ci.yml
```

Rationale:
- `internal/sysrepoc` is the **only** package that imports `C`/libsysrepo. It
  lives behind the `internal/sysrepoadapter` interface so the rest of the
  codebase is testable without cgo.
- `internal/schema` is the **only** package that imports
  `github.com/openconfig/goyang`. The same isolation principle.
- Everything else (`transport`, `framing`, `rpc`, `operations`) is pure Go
  and easily unit-tested with the sysrepo adapter mocked.

---

## 5. Key Data Flows

### 5.1 `get-config` (running)
1. SSH transport receives a chunked `<rpc>`.
2. `framing.Reader` reassembles the message; `rpc.Dispatch` parses it.
3. `operations/getconfig` is invoked with `source=running`.
4. `Session.SwitchDS(Running)` + `Adapter.GetItems("/")` returns the running
   tree as a slice of `DataNode` (or, phase 2, a streamed `lyd_node` view).
5. `filter/xpath` or `filter/subtree` selects the requested subset.
6. Result is serialized to NETCONF XML — by libyang when possible, else by
   a small Go encoder that walks the `DataNode` slice using the goyang
   schema for element ordering and namespace assignment.
7. `rpc.Reply` wraps it as `<rpc-reply message-id="…"><data>…</data></rpc-reply>`.

### 5.2 `get-schema`
1. Validate the requested module-name/revision against `schema.Cache`.
2. Look up the source text cached at load time.
3. Return `<data>…base64-or-CDATA…</data>` with the right content type.

### 5.3 `lock`/`unlock`
1. Translate datastore name → `sr_datastore_t`.
2. Call `sr_lock` / `sr_unlock` through the adapter; remember the lock in
   `Session.Locks` so `close-session` releases it automatically.

---

## 6. Cross-Cutting Concerns

### 6.1 Sessions & concurrency
- Each NETCONF session = 1 goroutine + 1 sysrepo session.
- Session-scoped mutex serializes RPCs (RFC 6241 §3.3).
- Global `SessionRegistry` (id → `*Session`) for `kill-session` and for
  SSH-channel-level observability.

### 6.2 Error handling
- Internal errors carry `Op`, `Kind`, `Cause` plus optional `yang.Node`.
- A single `toRpcError(err)` in `internal/rpc/error.go` converts them to the
  `<rpc-error>` payload with `error-tag`, `error-severity`, optional
  `error-app-tag`, `error-path`, `error-info`, and `error-message`.

### 6.3 Lifecycle
- `context.Context` flows from `main` → server → session → adapter. SIGHUP
  rebuilds the schema cache; SIGTERM triggers graceful shutdown (close
  listener, drain sessions, release locks).

### 6.4 Observability
- `expvar`/`pprof` endpoints off by default.
- Prometheus metrics: `sessions_active`, `rpc_total{op,status}`,
  `rpc_duration_seconds{op}`, `sysrepo_errors_total{kind}`.

### 6.5 Build
- `go build -tags cgo ./...` (sysrepo needs cgo).
- `//go:build !noci` guard for unit tests that don't need cgo.
- `make test` runs `go test ./internal/...` (mocked adapter); `make
  test-integration` brings up sysrepod in a container and exercises the
  full SSH path.

---

## 7. Why Not Just Use Netopeer2?

| Need                              | Netopeer2                  | confd                          |
|-----------------------------------|----------------------------|--------------------------------|
| Embeddable in a Go service        | no (C)                     | yes (library + binary)        |
| Iterate on protocol features      | C/libyang idioms           | Go interfaces + table-driven   |
| Share YANG with Go tooling        | requires shelling out      | goyang directly                |
| Lightweight single-binary deploy  | sysrepo+netopeer2+libyang  | sysrepo+confd                 |
| Test story for Go projects        | external                   | `go test` end-to-end          |

We are not replacing Netopeer2 for everyone — we are building a Go-friendly
NETCONF server whose first job is to read config/operational data from
sysrepo with the same on-wire semantics as Netopeer2.

---

## 8. Risks & Open Questions

1. **libyang XML serialization through cgo.** Passing `lyd_node*` across cgo
   for direct XML emission is the cleanest path but requires careful lifetime
   management. *Decision:* start with the Go-side encoder over `DataNode` for
   the MVP; add the cgo-direct path in phase 2 only if benchmarks justify it.
2. **goyang vs libyang divergence on schema features.** If goyang fails to
   parse a module sysrepo accepts (or vice versa), `<get-schema>` and
   capability advertisement can drift. *Mitigation:* treat sysrepo/libyang
   as authoritative for the data path and surface mismatches as a warning
   + an `unknown-schema` `<rpc-error>` for the affected `get-schema` call.
3. **sysrepo API stability.** cgo bindings pin to a specific sysrepo
   version; document the supported range and run the integration matrix in CI.
4. **NACM co-existence.** sysrepo has its own NACM (ietf-netconf-acm). For the
   read-only MVP, NACM is effectively `permit-all` from confd's side; phase 2
   integrates with sysrepo's NACM rather than re-implementing it.
5. **Default values / `with-defaults`.** sysrepo returns `lyd` with the
   configured default-handling mode. We expose `:with-defaults` only after we
   test the behavior end-to-end; for the MVP we return non-defaulted values
   exactly as Netopeer2 does with `report-all` semantics.

---

## 9. Milestones (MVP → 1.0)

| Milestone | Scope                                                  | Exit criteria                                                |
|-----------|--------------------------------------------------------|--------------------------------------------------------------|
| M0        | skeleton: ssh transport + framing + hello              | `nc` client can `<get/>` an empty datastore.                |
| M1        | `get`, `get-config`, `get-schema`, `lock`, sessions    | Passes Netopeer2's conformance tests for these ops.         |
| M2        | filters (subtree + xpath), with-defaults, error map   | Round-trips `ietf-system` / `ietf-interfaces` modules.      |
| M3        | `edit-config`, `<commit>`, `<discard-changes>`        | Netopeer2 `edit-config` tests pass against confd.           |
| M4        | NACM, notifications, `<action>`                        | Full RFC 6241 base compliance; tagged 1.0.                  |

---

## 10. Summary

`confd` is a Go NETCONF server whose MVP focuses on **read-only retrieval of
configuration and operational data** from sysrepo. It uses **goyang** as the
schema layer (capabilities, `get-schema`, filter validation) and **sysrepo +
libyang** as the data layer (datastore access, XML serialization, error
formatting). The two are deliberately separated by interfaces so that each
can be swapped, mocked, or replaced (notably a future pure-Go libyang or a
pure-Go sysrepo IPC client) without touching the NETCONF protocol logic.
