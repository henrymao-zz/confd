# confd — Go-based NETCONF Server & Single-Daemon Design

> `confd` is a Go-based NETCONF server backed by **sysrepo**, using
> **[goyang](https://github.com/openconfig/goyang)** as the YANG schema
> parser. It is a lighter, Go-native alternative to Netopeer2. It serves
> NETCONF over SSH (RFC 6241/6242) and, in a single process, replaces both
> `netopeer2-server` and `sysrepo-plugind` by embedding the plugin host
> that loads Telekom sysrepo-plugins.

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
- **Single-daemon mode**: confd replaces `sysrepo-plugind` by loading
  `libsrplg-*.so` plugins via `dlopen` in the same process.

### 1.2 Non-Goals (for MVP)
- `<edit-config>` / `<copy-config>` / `<delete-config>` / `<commit>` —
  deferred to phase 2 (they require full NACM + transaction plumbing).
- Notifications / `<create-subscription>` — phase 3.
- Call-home (RFC 8071), TLS transport (RFC 7589) — phase 4.
- RESTCONF — separate project, out of scope here.
- Reimplementing any plugin in Go.
- Replacing libyang/libsysrepo with a pure-Go datastore.

### 1.3 Phase 2+ Roadmap (sketch)
1. `<edit-config>` + transactions (`<lock>`, `<commit>`, `<discard-changes>`).
2. NACM (RFC 6536) enforcement on edit operations.
3. `<notification>` replay via sysrepo notification store.
4. `<validate>`, `<action>` (YANG 1.1 RPC/action) dispatch.
5. TLS / call-home / clustered multi-tenant operation.

---

## 2. Key Insight: sysrepo is a SHM Library, Not a Server

sysrepo has **no datastore server process**. It is a shared-memory library
architecture:

- `sr_connect()` (`src/sysrepo.c:197`) opens/creates POSIX SHM files
  (`/dev/shm/sr_main`, `sr_ext`, `sr_mod`) under the configured repository
  path. There is no `listen()`/`accept()`/socket — coordination is via SHM
  + `pthread` mutexes + per-connection lock files.
- Every process that calls `sr_connect()` is a peer; the first one to
  acquire the create-lock initializes the SHM, subsequent ones attach.
- The "daemons" sysrepo ships are all **consumers** of the SHM datastore,
  not servers:
  - `sysrepo-plugind` — loads plugins; calls `sr_connect` + `sr_session_start`
    + `sr_plugin_init_cb` + `while(!exit) cond_wait` + cleanup.
  - `sysrepo-notifd` — RFC 8639 notification relay; also just a `sr_connect` peer.
  - `netopeer2-server` — the NETCONF front-end; also just a `sr_connect` peer.

So "running sysrepo in a single process as confd" means **confd is the
`sr_connect` peer that also owns the plugin lifecycle and serves NETCONF**,
instead of spreading that across `netopeer2-server` + `sysrepo-plugind` +
optionally `sysrepo-notifd`.

| Component | Today (multi-process) | In `confd` (single process) | Mechanism |
|---|---|---|---|
| NETCONF server | `netopeer2-server` | **confd** | Go server |
| sysrepo datastore | SHM files, no daemon | **SHM files, no daemon** | `sr_connect` opens them |
| sysrepo client lib (libsysrepo) | linked into every consumer | linked **once** into confd | cgo |
| sysrepo plugins | `sysrepo-plugind` loads `libsrplg-*.so` | **confd** loads them | `dlopen` plugin host (§6) |
| `sysrepo-notifd` (RFC 8639) | separate daemon | absorbed **or** external | build tag (§8) |

---

## 3. High-Level Architecture

```
                 ┌──────────────────────────── confd (one process) ────────────────────────────┐
                 │                                                                            │
    SSH client    │  ┌────────────┐   ┌──────────────┐   ┌──────────┐                          │
    ──────────────┼─▶│ transport  │──▶│  framing +   │──▶│  rpc     │──▶ operations ──┐         │
    (RFC 6242)    │  │  ssh       │   │  <hello>     │   │ dispatch │                  │         │
                 │  └────────────┘   └──────────────┘   └──────────┘                  ▼        │
                 │                                                                 sysrepo   │
                 │  ┌──────────────────────────────────────┐                  adapter (cgo)     │
                 │  │ plugin host (replaces                │                         │          │
                 │  │ sysrepo-plugind)                     │   sr_connect (opens SHM)│          │
                 │  │  • dlopen libsrplg-ietf-system.so    │─────────────────────────┤          │
                 │  │  • dlopen libsrplg-ietf-ifaces.so    │                         ▼          │
                 │  │  • per-plugin sr_session_start       │                  ┌──────────┐    │
                 │  │  • sr_plugin_init_cb (starts loop)   │                  │libsysrepo│    │
                 │  │  • on SIGTERM: cleanup in reverse    │                  │(in-proc) │    │
                 │  └──────────────────────────────────────┘                  └────┬─────┘    │
                 │   schema.Cache (goyang) ◀─── filter validation                   │ SHM      │
                 │                                                                    ▼          │
                 │   Go runtime                                              /dev/shm/sr_*      │
                 │   (signal handling, graceful shutdown)                                     │
                 └────────────────────────────────────────────────────────────────────────────┘
```

### 3.1 Why goyang + sysrepo (and the impedance mismatch)

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

## 4. Component Breakdown

### 4.1 `transport` — NETCONF over SSH
- Wraps `golang.org/x/crypto/ssh` for the SSH channel.
- Implements RFC 6242 framing: chunked framing, plus the base:1.0
  `\n]]>]]>\n` fallback during `<hello>`.
- Handles SSH `subsystem` requests (sent by `ssh -s ... netconf`) and
  `exec` requests; rejects unknown channel requests.
- Configurable: host key path, bind address, port (default 830), password auth.
- In-memory `Pipe` transport for tests.
- Interface:
  ```go
  type Transport interface {
      ReadMessage() ([]byte, error)
      WriteMessage(msg []byte) error
      Framing() (*framing.Reader, *framing.Writer)
      PeerUser() string
      Close() error
  }
  ```

### 4.2 `framing` — RFC 6242 message codec
- Pure-Go chunked framing parser. Knows how to upgrade from 1.0 to 1.1 once
  both sides advertise the `:base:1.1` capability.
- Exposes `Reader` / `Writer` that operate on top of the SSH channel's
  `io.Reader`/`io.Writer`.

### 4.3 `hello` — capability negotiation
- On connect, send `<hello>` with:
  - `:base:1.1` (and `:base:1.0` for back-compat).
  - one URI per YANG module in the schema cache (module + revision + features).
  - `:with-defaults` (RFC 6131), `:validate:1.1`, `:validate:1.0` — only if
    actually implemented; we do *not* advertise capabilities we don't serve.
- Parse peer `<hello>`, record the agreed capabilities, and switch the
  framing to 1.1 when both sides support it.

### 4.4 `rpc` — message routing
- Decodes `<rpc message-id="..."> ... </rpc>`.
- Looks up the inner operation QName against a `map[string]Handler`.
- Each `Handler` returns either an `<ok/>`, a data tree reply, or an
  `<rpc-error>` with the right `error-tag`/`error-severity`/`error-info`.
- All errors follow RFC 6241 Appendix A error taxonomy.

### 4.5 `operations` — protocol operation handlers
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

#### 4.5.1 Filter handling
- **Subtree filter (RFC 6241 §6.4)**: convert to a libyang filter via
  `lyd_new_path` + select, or fall back to a Go-side walker when libyang
  cannot express the merge semantics. The MVP implements "Containment" and
  "Selection nodes" exactly as in the RFC; `content-match` nodes are
  supported for the common `leaf`/`leaf-list` case.
- **XPath filter (`type="xpath"`)**: pass the XPath directly to
  `sr_get_items` / `sr_get_items_iter` with the XPath; libyang does the rest.
- Any filter path is first validated against the goyang schema cache so an
  unknown path yields `<rpc-error error-tag="unknown-element">`.

### 4.6 `schema` — goyang cache
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

### 4.7 `sysrepoadapter` — cgo bindings to libsysrepo
The adapter exposes a Go-native interface that hides `lyd_node` and
`sr_session_*`:

```go
type Adapter interface {
    Connect(ctx context.Context) (Conn, error)
}

type Conn interface {
    ListModules(ctx context.Context) ([]ModuleInfo, error)
    OpenSession(ctx context.Context, user string) (Session, error)
    Close() error
}

type Session interface {
    SwitchDS(ds Datastore) error
    CurrentDS() Datastore
    Get(ctx context.Context, xpath string) (*DataNode, error)
    Lock(ds Datastore) error
    Unlock(ds Datastore) error
    Close() error
}
```

Two implementations:
- `Mock` — in-memory YANG-modeled tree; used by tests and as the default
  when libsysrepo headers aren't installed (pure Go, no cgo).
- `CGo` — selected by the `sysrepo` build tag; a thin cgo shim over
  libsysrepo (`sr_connect`, `sr_session_start`, `sr_session_switch_ds`,
  `sr_get_items`, `sr_lock`, `sr_unlock`, `sr_session_stop`,
  `sr_disconnect`). Also implements `RawConnProvider` to expose the raw
  `sr_conn_ctx_t*` to the plugin host.

`DataNode` is a thin Go value object; the underlying `lyd_node*` is owned by
the adapter for the lifetime of the call and freed before returning. For the
MVP we return Go-side parsed values; in phase 2 we'll let the operations
layer request a `lyd_node`-backed tree directly when XML serialization needs
it (so we don't double-encode through Go).

### 4.8 `pluginhost` — replaces `sysrepo-plugind`
The plugin host is the "single daemon" piece. It `dlopen`s the shipped
`libsrplg-*.so` artifacts, gives each a `sr_session_start`, calls
`sr_plugin_init_cb` (which starts the plugin's own event loop), and on
shutdown calls `sr_plugin_cleanup_cb` in reverse load order.

```go
type Host interface {
    Start(conn sysrepoadapter.Conn, specs []Spec) error
    Stop() error
    Names() []string
}
```

Three implementations:
- `MockHost` — pure-Go mock for tests (records loaded plugins, supports
  `fail-` prefix for init-failure simulation).
- `NoopHost` — default build (returns `ErrNotAvailable`).
- `CGoHost` — behind `sysrepo` build tag: `dlopen` + `sr_session_start` +
  `sr_plugin_init_cb` on Start; `sr_plugin_cleanup_cb` + `sr_session_stop` +
  `dlclose` in reverse order on Stop.

### 4.9 `config` — runtime configuration
CLI flags plus an optional YAML file:
```yaml
listen:
  ssh:
    bind: 0.0.0.0:830
    host_key: /etc/confd/host_key
    password: confd
schema:
  yang_paths: [/etc/confd/yang, /usr/share/yang/modules]
adapter: mock            # or "sysrepo"
sysrepo_socket: ""
plugins_dir: /usr/lib/confd/plugins
plugins: [ietf-system, ietf-interfaces]
```

### 4.10 `logging`
- `log/slog` structured logger.
- Per-session context fields: `session_id`, `peer_user`, `peer_addr`,
  `message_id`, `operation`.
- An opt-in raw-RPC dump (gated behind `log.level=debug`) for development.

---

## 5. Key Data Flows

### 5.1 `get-config` (running)
1. SSH transport receives a chunked `<rpc>`.
2. `framing.Reader` reassembles the message; `rpc.Dispatch` parses it.
3. `operations/getconfig` is invoked with `source=running`.
4. `Session.SwitchDS(Running)` + `Adapter.Get("/")` returns the running
   tree as a `DataNode` tree.
5. `filter/xpath` or `filter/subtree` selects the requested subset.
6. Result is serialized to NETCONF XML — by libyang when possible, else by
   a small Go encoder that walks the `DataNode` tree using the goyang
   schema for element ordering and namespace assignment.
7. `rpc.Reply` wraps it as `<rpc-reply message-id="…"><data>…</data></rpc-reply>`.

### 5.2 `get-schema`
1. Validate the requested module-name/revision against `schema.Cache`.
2. Look up the source text cached at load time.
3. Return `<data>…CDATA…</data>` with the right content type.

### 5.3 `lock`/`unlock`
1. Translate datastore name → `sr_datastore_t`.
2. Call `sr_lock` / `sr_unlock` through the adapter; remember the lock in
   `Session.Locks` so `close-session` releases it automatically.

---

## 6. Plugin Host Design (Single-Daemon)

### 6.1 The plugin contract: the key enabler

Every Telekom plugin ships as a `libsrplg-<name>.so` that exports exactly:

```c
int  sr_plugin_init_cb   (sr_session_ctx_t *session, void **priv);
void sr_plugin_cleanup_cb(sr_session_ctx_t *session, void  *priv);
```

- `init` registers all sysrepo subscriptions (operational-data providers,
  change callbacks, RPC handlers) and **starts the plugin's own event loop
  on an internal thread** (sdbus `IoContext`, libnl readers, timers).
- `cleanup` joins that loop and unregisters.
- `sysrepo-plugind`'s entire job is `dlopen` → `init` → `cond_wait` →
  `cleanup` → `dlclose`.

**Implication:** a Go host only has to own the **lifecycle** (connect /
per-plugin session / init / cleanup / shutdown). It never drives a plugin's
event loop. This is what makes a Go host a drop-in replacement for
`sysrepo-plugind`.

### 6.2 Loading: `dlopen`, not static link

Static-linking C++ plugins into Go is fragile (C++ static-init order,
duplicate sdbus-c++ singletons, ODR violations). We `dlopen` the shipped
`libsrplg-*.so` artifacts exactly as `sysrepo-plugind` does:

```go
type Spec struct {
    Name string   // "ietf-system"
    Path string   // /usr/lib/confd/plugins/libsrplg-ietf-system.so
}
```

The cgo layer exposes thin wrappers:
```c
void *cf_dlopen(const char *path);                              // handle or NULL
int   cf_call_init(void *h, sr_session_ctx_t *s, void **priv);  // dlsym + call
void  cf_call_cleanup(void *h, sr_session_ctx_t *s, void *priv);
void  cf_dlclose(void *h);
```

### 6.3 Session model: one connection, one session per plugin

```
sr_connect  →  conn_ctx
for each plugin:
    sr_session_start(conn_ctx, SR_DS_RUNNING, &plugin_sess)
    sr_plugin_init_cb(plugin_sess, &priv)    // starts plugin's internal loop
    // keep (plugin_sess, priv) for cleanup
```

Rationale: isolation (a plugin that errors its session doesn't break
NETCONF's sessions); matches `sysrepo-plugind`; cheap (SHM sessions are
lightweight).

### 6.4 Event-loop ownership

**The host owns none.** Each plugin's `init` starts its own loop. The
host:

- Calls `init` on a `runtime.LockOSThread()` thread (so cgo + sdbus-c++
  thread-local dispatchers are stable), then `UnlockOSThread`. The
  plugin keeps using its own internal threads afterwards.
- Does **not** run `sysrepo-plugind`'s `while(!exit) cond_wait` loop.
  Go's main goroutine blocks on a shutdown `context.Context`; plugins
  keep themselves alive via their own loops.

### 6.5 Shutdown ordering (critical)

```
SIGINT/SIGTERM
  → stop accepting NETCONF transports (drain in-flight RPCs, timeout)
  → for each plugin in REVERSE load order:
        cf_call_cleanup(handle, session, priv)   // joins plugin's loop
        sr_session_stop(session)
        cf_dlclose(handle)
  → sr_disconnect()
  → process exit
```

Rationale: in-flight RPCs may be served by plugin-provided operational
data; once NETCONF is drained it's safe to tear plugins down. Reverse
order handles plugins that depend on others (routing depends on
interfaces) cleanly.

### 6.6 Failure handling

- A plugin whose `init` returns non-zero is **disabled, logged, and
  skipped**; confd keeps starting the rest. (`sysrepo-plugind` aborts;
  we improve on it.)
- A plugin that crashes (SEGV in its loop) takes the process down in
  v1. v2 hardening: fork+supervise per plugin — but that reintroduces
  helper processes, so deferred.

### 6.7 Integration with the server

`server.Config` gains `PluginHost` + `PluginSpecs` fields:
- `server.New` calls `host.Start(conn, specs)` after `Connect` and
  before `ListenAndServe`.
- `server.Close` calls `host.Stop()` before `conn.Close()` (plugins
  torn down before datastore).

`cmd/confd` gains:
- `--plugins-dir=/usr/lib/confd/plugins` (where `libsrplg-*.so` live)
- `--plugin=ietf-system` (repeatable allowlist; empty = load all)
- `discoverPlugins()` builds `[]pluginhost.Spec` from the directory

---

## 7. Cross-Cutting Concerns

### 7.1 Sessions & concurrency
- Each NETCONF session = 1 goroutine + 1 sysrepo session.
- Session-scoped mutex serializes RPCs (RFC 6241 §3.3).
- Global `SessionRegistry` (id → `*Session`) for `kill-session` and for
  SSH-channel-level observability.

### 7.2 Error handling
- Internal errors carry `Op`, `Kind`, `Cause` plus optional `yang.Node`.
- A single `toRpcError(err)` in `internal/rpc/rpc.go` converts them to the
  `<rpc-error>` payload with `error-tag`, `error-severity`, optional
  `error-app-tag`, `error-path`, `error-info`, and `error-message`.

### 7.3 Lifecycle
- `context.Context` flows from `main` → server → session → adapter. SIGHUP
  rebuilds the schema cache; SIGTERM triggers graceful shutdown (close
  listener, drain sessions, release locks, stop plugins, disconnect).

### 7.4 Observability
- `expvar`/`pprof` endpoints off by default.
- Prometheus metrics: `sessions_active`, `rpc_total{op,status}`,
  `rpc_duration_seconds{op}`, `sysrepo_errors_total{kind}`.

### 7.5 Build
- `go build ./...` — pure Go (uses the Mock adapter, NoopHost).
- `go build -tags sysrepo ./...` — cgo build against libsysrepo (needs
  sysrepo headers + dlopen).
- `make test` runs `go test ./...` (mocked adapter); all packages pass
  without cgo or sysrepo installed.

---

## 8. `sysrepo-notifd` (RFC 8639)

`sysrepo-notifd` implements configured subscription delivery (RFC 8639)
— a UDP notification relay. It is **optional**:

- **v1 (default): leave external.** confd does not yet implement
  `<create-subscription>`; when it does, the notification relay can
  stay as a separate process or be absorbed.
- **v2 (build tag `notifd`): absorb into confd.** The notifd is also a
  `sr_connect` peer with its own event loop, same pattern as the plugin
  host.

Either way, notifd is orthogonal to the "single daemon" goal — it serves
a NETCONF feature confd doesn't implement yet.

---

## 9. YANG Provisioning

sysrepo needs YANG modules installed in its datastore before plugins can
subscribe. confd **self-provisions on first boot**:

- confd ships a manifest (`/etc/confd/plugins.yaml`) listing each
  enabled plugin and its YANG files + features to enable (mirrors the
  per-plugin `sysrepoctl -i … --enable-feature …` blocks in the Telekom
  README).
- On startup, confd queries `sr_get_module_list`; for any manifest module
  that's missing or lacks a required feature, confd uses the
  `sr_install_module` API (or shells out to `sysrepoctl`) to install it.
- Idempotent; matches what a packaging `%post` script would do, but
  keeps "single daemon" honest — no separate setup service.

---

## 10. Build & Packaging

The single-binary promise is **runtime**, not compile-time: the C++
plugins are still built by their own CMake (they need sdbus-c++, libnl,
etc.). The confd build produces:

```
confd                                  # one Go binary (links libsysrepo via cgo)
/usr/lib/confd/plugins/
  libsrplg-ietf-system.so              # from telekom/sysrepo-plugins build
  libsrplg-ietf-interfaces.so
  libsrplg-ietf-routing.so
  libsrplg-ietf-hardware.so
  libsrplg-os-metrics.so
  libsrplg-ietf-access-control-list.so
  libsrplg-ieee802-dot1q-bridge.so
```

**Deployment = `apt install confd && systemctl start confd`.** One unit,
one process, one log. `sysrepo-plugind` and `netopeer2-server` units are
not installed.

The `Makefile`:
```
make build       # pure Go (mock adapter)
make sysrepo     # cgo build against libsysrepo
make test        # unit + integration tests
make test-race   # with the race detector
make vet
make plugins     # cmake build of telekom/sysrepo-plugins -> build/plugins/*.so
make install     # go install confd + cp plugins to $(DESTDIR)/usr/lib/confd/plugins
```

---

## 11. Repository Layout

```
confd/
├── cmd/confd/             # entrypoint (serve, schema-list)
├── internal/
│   ├── config/            # flags + defaults
│   ├── transport/         # SSH + in-memory pipe transports
│   │   ├── transport.go   # Transport interface
│   │   └── ssh.go         # RFC 6242 SSH transport + subsystem handling
│   ├── framing/           # RFC 6242 chunked framing
│   ├── hello/             # <hello> + capability negotiation
│   ├── rpc/               # <rpc> parse, dispatch, <rpc-error>
│   ├── operations/        # get, get-config, get-schema, lock, unlock, sessions
│   ├── schema/            # goyang-backed cache (the only goyang importer)
│   ├── sysrepoadapter/    # Adapter interface + Mock + CGo (build tag)
│   │   ├── adapter.go     # Adapter, Conn, Session, RawConnProvider
│   │   ├── mock.go        # in-memory mock adapter
│   │   ├── cgo_stub.go    # real cgo adapter (behind sysrepo tag)
│   │   └── cgo_nosysrepo.go  # stub (default build)
│   ├── pluginhost/        # replaces sysrepo-plugind
│   │   ├── host.go        # Host interface + NoopHost
│   │   ├── mock.go        # MockHost for tests
│   │   ├── host_sysrepo.go # CGoHost (dlopen, behind sysrepo tag)
│   │   ├── new.go         # New() -> NoopHost (default)
│   │   └── new_sysrepo.go # New() -> CGoHost (sysrepo tag)
│   ├── data/              # DataNode -> NETCONF XML encoder
│   └── server/            # wiring + ServeTransport / ListenAndServe
├── yang/confd-test.yang   # in-tree YANG module used by tests
├── go.mod
├── Makefile
└── README.md
```

Rationale:
- `internal/sysrepoadapter` is the **only** package that imports `C`/libsysrepo
  (via the `sysrepo` build tag). The `Mock` adapter is pure Go and used by
  all tests.
- `internal/schema` is the **only** package that imports
  `github.com/openconfig/goyang`. The same isolation principle.
- `internal/pluginhost` is the **only** package that does `dlopen` (via the
  `sysrepo` build tag). `MockHost` and `NoopHost` are pure Go.
- Everything else (`transport`, `framing`, `rpc`, `operations`, `server`)
  is pure Go and easily unit-tested with the sysrepo adapter and plugin
  host mocked.

---

## 12. Key Data Flows (Single-Daemon Lifecycle)

### 12.1 Startup
```
main()
  → config.FromFlags()
  → adapter = NewCGo(socket) or NewMock(nil)
  → pluginHost = pluginhost.New()  (CGoHost or NoopHost)
  → specs = discoverPlugins(pluginsDir, allowlist)
  → server.New(ctx, Config{Adapter, PluginHost, PluginSpecs, ...})
      → adapter.Connect()          → sr_connect (opens SHM)
      → pluginHost.Start(conn, specs)
          → for each spec: dlopen → sr_session_start → sr_plugin_init_cb
      → operations.Register(dispatch, deps)
  → server.ListenAndServe(ctx, sshCfg)
      → accept SSH connections → ServeTransport per session
```

### 12.2 Shutdown (SIGINT/SIGTERM)
```
signal.NotifyContext cancels ctx
  → listener.Close() (stops accepting)
  → drain in-flight NETCONF sessions
  → server.Close()
      → pluginHost.Stop()
          → for each plugin in REVERSE: sr_plugin_cleanup_cb → sr_session_stop → dlclose
      → conn.Close()  → sr_disconnect
```

---

## 13. Risks & Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| C++ plugin crashes (sdbus/libnl) abort the Go process | NETCONF server dies with the plugin | v1: accept (same as `sysrepo-plugind`); v2: fork+supervise per plugin |
| Thread-local state in sdbus-c++ vs Go's `LockOSThread` | subtle deadlocks | keep init/cleanup on locked threads; never call cgo from arbitrary goroutines |
| `sysrepod` still required for v1 | not truly single-process | documented; there is no `sysrepod` — sysrepo is SHM; confd is the only peer |
| Plugin load order / inter-plugin dependencies | init failures | ordered manifest; reverse-order cleanup; per-plugin disable on init error |
| YANG module version skew between confd's goyang cache and sysrepo SHM | capability drift | confd loads its goyang cache **from sysrepo's installed modules** (already the design), so they can't diverge |
| dlopen + static libsysrepo symbol clashes | duplicate `sr_*` symbols | link libsysrepo **once** (in confd); plugins link it dynamically; verify with `ldd`/`nm` in CI |
| SIGPIPE from a half-closed SSH channel | crash | Go runtime ignores SIGPIPE on non-stdout writes; verify, add `signal.Ignore(syscall.SIGPIPE)` |
| First `sr_connect` initializes SHM; race with a concurrent `sysrepo-plugind` | double-init | don't run `sysrepo-plugind` alongside confd; confd is its replacement. Document this. |
| SHM lifecycle: confd crash leaves stale SHM | next restart re-attaches | sysrepo already handles this (`sr_connect` re-creates/attaches); no new risk vs. today |
| libyang XML serialization through cgo | lifetime management | start with Go-side encoder over `DataNode` for MVP; add cgo-direct path in phase 2 if benchmarks justify |
| goyang vs libyang divergence on schema features | capability drift | treat sysrepo/libyang as authoritative for the data path; surface mismatches as warning + `<rpc-error>` |
| NACM co-existence | sysrepo has its own NACM | for read-only MVP, NACM is `permit-all` from confd's side; phase 2 integrates with sysrepo's NACM |

---

## 14. Why Not Just Use Netopeer2?

| Need                              | Netopeer2                  | confd                          |
|-----------------------------------|----------------------------|--------------------------------|
| Embeddable in a Go service        | no (C)                     | yes (library + binary)        |
| Iterate on protocol features      | C/libyang idioms           | Go interfaces + table-driven   |
| Share YANG with Go tooling        | requires shelling out      | goyang directly                |
| Single-daemon (NETCONF + plugins) | 2+ processes               | 1 process                     |
| Lightweight single-binary deploy  | sysrepo+netopeer2+libyang  | sysrepo+confd                 |
| Test story for Go projects        | external                   | `go test` end-to-end          |

We are not replacing Netopeer2 for everyone — we are building a Go-friendly
NETCONF server whose first job is to read config/operational data from
sysrepo with the same on-wire semantics as Netopeer2, and whose second job
is to absorb `sysrepo-plugind` into the same process.

---

## 15. Milestones

| Phase | Scope | Exit criteria |
|---|---|---|
| **M0** | skeleton: ssh transport + framing + hello | `nc` client can `<get/>` an empty datastore. |
| **M1** | `get`, `get-config`, `get-schema`, `lock`, sessions | Passes Netopeer2's conformance tests for these ops. |
| **M2** | filters (subtree + xpath), with-defaults, error map | Round-trips `ietf-system` / `ietf-interfaces` modules. |
| **M3** | `edit-config`, `<commit>`, `<discard-changes>` | Netopeer2 `edit-config` tests pass against confd. |
| **M4** | NACM, notifications, `<action>` | Full RFC 6241 base compliance; tagged 1.0. |
| **P1** | cgo datastore adapter real (`sr_connect`, `sr_session_*`, `sr_get_items`, `sr_lock`) | confd reads live sysrepo running/operational data over SSH. |
| **P2** | plugin host: `dlopen` + per-plugin session + init/cleanup + ordered shutdown | one Telekom plugin (`ietf-system`) loaded by confd; `<get>` returns real hostname/timezone. |
| **P3** | multi-plugin, manifest, `--plugins-dir`/`--plugin`, YANG self-provisioning | all 7 Telekom plugins load; `systemctl start confd` is the only step. |
| **P4** | (optional) absorb `sysrepo-notifd` behind `notifd` build tag | RFC 8639 configured subscriptions work without a separate process. |
| **P5** | (optional) per-plugin fork+supervise hardening | a crashing plugin no longer takes down NETCONF. |

---

## 16. Summary

`confd` is a Go NETCONF server whose MVP focuses on **read-only retrieval of
configuration and operational data** from sysrepo. It uses **goyang** as the
schema layer (capabilities, `get-schema`, filter validation) and **sysrepo +
libyang** as the data layer (datastore access, XML serialization, error
formatting). In single-daemon mode it also **replaces `sysrepo-plugind`** by
hosting Telekom sysrepo-plugins via `dlopen` in the same process. The three
layers (schema, data, plugins) are deliberately separated by interfaces so
that each can be swapped, mocked, or replaced without touching the NETCONF
protocol logic.