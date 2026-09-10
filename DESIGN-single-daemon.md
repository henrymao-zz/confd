# confd Single-Daemon Design: Embedding sysrepo + sysrepo-plugins

> Companion to `DESIGN.md`. Goal: make `confd` a **single daemon** that
> bundles the NETCONF server, the sysrepo datastore, and the Telekom
> sysrepo-plugins into one process — one binary, one lifecycle, one set
> of signals, one log stream.

## 0. The key correction: there is no `sysrepod`

The earlier draft of this document assumed a separate `sysrepod` datastore
daemon. **That is wrong.** sysrepo has no datastore server process.

sysrepo is a **shared-memory library architecture**, not a client-server one:

- `sr_connect()` (`src/sysrepo.c:197`) opens/creates POSIX SHM files
  (`/dev/shm/sr_main`, `sr_ext`, `sr_mod`) under the configured repository
  path. There is no `listen()`/`accept()`/socket — coordination is via SHM
  + `pthread` mutexes + per-connection lock files.
- Every process that calls `sr_connect()` is a peer; the first one to
  acquire the create-lock initializes the SHM, subsequent ones attach.
- The "daemons" sysrepo ships are all **consumers** of the SHM datastore,
  not servers:
  - `sysrepo-plugind` — loads plugins; calls `sr_connect` + `sr_session_start`
    + `sr_plugin_init_cb` + `while(!exit) cond_wait` + cleanup. See
    `src/executables/sysrepo-plugind.c:560-600`.
  - `sysrepo-notifd` — RFC 8639 notification relay; also just a `sr_connect` peer.
  - `netopeer2-server` — the NETCONF front-end; also just a `sr_connect` peer.

So "running sysrepo in a single process as confd" does not mean embedding a
daemon — it means **confd is the `sr_connect` peer that also owns the plugin
lifecycle and serves NETCONF**, instead of spreading that across
`netopeer2-server` + `sysrepo-plugind` (+ optionally `sysrepo-notifd`).

This is strictly simpler than the previous draft assumed.

## 1. What we are (and are not) bundling

| Component | Today (multi-process) | In `confd` (single process) | Mechanism |
|---|---|---|---|
| NETCONF server | `netopeer2-server` | **confd** (already) | Go server from `DESIGN.md` |
| sysrepo datastore | SHM files, no daemon | **SHM files, no daemon** | unchanged — `sr_connect` opens them |
| sysrepo **client lib** (libsysrepo) | linked into every consumer | linked **once** into confd | cgo |
| sysrepo **plugins** | `sysrepo-plugind` loads `libsrplg-*.so` | **confd** loads them | `dlopen` plugin host (§5) |
| `sysrepo-notifd` (RFC 8639) | separate daemon | absorbed **or** external | build tag (§7) |

**Non-goals**
- Reimplementing any plugin in Go.
- Replacing libyang/libsysrepo with a pure-Go datastore.
- RESTCONF / Call-home (orthogonal, out of scope).

## 2. The plugin contract: the key enabler

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
  `cleanup` → `dlclose` (`src/executables/sysrepo-plugind.c:560-600`).

**Implication:** a Go host only has to own the **lifecycle** (connect /
per-plugin session / init / cleanup / shutdown). It never drives a plugin's
event loop. This is what makes a Go host a drop-in replacement for
`sysrepo-plugind`.

## 3. Architecture (single process)

```
                 ┌──────────────────────────── confd (one process) ────────────────────────────┐
                 │                                                                            │
   NETCONF ─────▶│  transport ─▶ framing ─▶ rpc ─▶ operations ─▶ sysrepoadapter (cgo)         │
   (SSH)         │                                          │                                  │
                 │                                          │                                  │
                 │  ┌──────────────────────────────────────┐ │   sr_connect (opens SHM)        │
                 │  │ plugin host (cgo, replaces            │ │       │                          │
                 │  │ sysrepo-plugind)                     │ └───────┼──────────────────────────│
                 │  │  • dlopen libsrplg-ietf-system.so     │         │                          │
                 │  │  • dlopen libsrplg-ietf-ifaces.so     │         ▼                          │
                 │  │  • dlopen libsrplg-ietf-routing.so …   │   ┌───────────────┐               │
                 │  │  • per-plugin sr_session_start         │   │  libsysrepo   │               │
                 │  │  • sr_plugin_init_cb (starts loop)    │   │  (in-process, │               │
                 │  │  • on SIGTERM: cleanup in reverse     │   │   SHM peer)   │               │
                 │  └──────────────────────────────────────┘   └───────┬───────┘               │
                 │                                                     │                         │
                 │   Go runtime                                        │ SHM files               │
                 │   (signal handling, graceful shutdown)              ▼                         │
                 │                                        /dev/shm/sr_main, sr_ext, sr_mod      │
                 └────────────────────────────────────────────────────────────────────────────┘
```

There is **no separate datastore process**. confd calls `sr_connect()`,
which opens the SHM. The plugins (loaded by confd) and the NETCONF
operations (served by confd) share the same process's `libsysrepo` and
coordinate through SHM + mutexes — exactly as `netopeer2-server` and
`sysrepo-plugind` do today as separate processes, but without the IPC
boundary.

### cgo responsibilities (all in `internal/sysrepocgo`)

1. **Datastore peer** — `sr_connect` / `sr_session_start` /
   `sr_get_items` / `sr_lock` / `sr_disconnect`. Already the `CGo`
   adapter from `DESIGN.md`.
2. **Plugin host** — `dlopen` / `dlsym` / per-plugin session /
   `init`/`cleanup` lifecycle. Replaces `sysrepo-plugind`.

Both share one `sr_conn_ctx_t` (the SHM handle) with independent
`sr_session_ctx_t`s — matching how `sysrepo-plugind` and `netopeer2-server`
share SHM today.

## 4. Why this is safe (no datastore daemon to break)

Because sysrepo is SHM-based, the things that would normally make embedding
a server risky don't apply:

| Concern with embedding a server | sysrepo reality |
|---|---|
| Two servers fighting over a listen port | no port; SHM + file locks |
| Need to fork/daemonize | `sysrepo-plugind` daemonizes itself, but `sr_connect` works fine in foreground; Go doesn't daemonize |
| Server crash loses state | state lives in SHM files on disk, not in the process; a crashed confd restarts and re-attaches |
| Multi-process concurrency | already handled by sysrepo's SHM locking; confd is just another peer |

The only lifecycle difference from running `netopeer2-server` +
`sysrepo-plugind` separately is that **one process** holds both the NETCONF
listener and the plugin threads. The SHM coordination is identical.

## 5. Plugin host design

### 5.1 Loading: `dlopen`, not static link

Static-linking C++ plugins into Go is fragile (C++ static-init order,
duplicate sdbus-c++ singletons, ODR violations). We `dlopen` the shipped
`libsrplg-*.so` artifacts exactly as `sysrepo-plugind` does:

```go
// internal/pluginhost/host.go (Go side)
type Spec struct {
    Name string   // "ietf-system"
    Path string   // /usr/lib/confd/plugins/libsrplg-ietf-system.so
}
type Loaded struct {
    Spec
    handle  unsafe.Pointer // dlopen handle (opaque)
    priv    unsafe.Pointer // plugin's private_data
    session unsafe.Pointer // sr_session_ctx_t*
}
```

The cgo layer exposes thin wrappers:
```c
void *cf_dlopen(const char *path);                              // handle or NULL
int   cf_call_init(void *h, sr_session_ctx_t *s, void **priv);  // dlsym + call
void  cf_call_cleanup(void *h, sr_session_ctx_t *s, void *priv);
void  cf_dlclose(void *h);
```

### 5.2 Session model: one connection, one session per plugin

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

### 5.3 Event-loop ownership

**The host owns none.** Each plugin's `init` starts its own loop. The
host:

- Calls `init` on a `runtime.LockOSThread()` thread (so cgo + sdbus-c++
  thread-local dispatchers are stable), then `UnlockOSThread`. The
  plugin keeps using its own internal threads afterwards.
- Does **not** run `sysrepo-plugind`'s `while(!exit) cond_wait` loop.
  Go's main goroutine blocks on a shutdown `context.Context`; plugins
  keep themselves alive via their own loops.

### 5.4 Shutdown ordering (critical)

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

### 5.5 Failure handling

- A plugin whose `init` returns non-zero is **disabled, logged, and
  skipped**; confd keeps starting the rest. (`sysrepo-plugind` aborts;
  we improve on it.)
- A plugin that crashes (SEGV in its loop) takes the process down in
  v1. v2 hardening: fork+supervise per plugin — but that reintroduces
  helper processes, so deferred (§9).

## 6. Integration with the existing `sysrepoadapter`

The `CGo` struct in `internal/sysrepoadapter/adapter.go` gains a plugin
host:

```
CGo
 ├── Connect(ctx)  → sr_connect (opens SHM), store conn, start plugin host
 ├── OpenSession   → sr_session_start (for per-RPC operations)
 ├── pluginHost    → *pluginhost.Host (new field)
 └── Close()       → pluginHost.Stop() then sr_disconnect
```

`cmd/confd` gains:
- `--plugins-dir=/usr/lib/confd/plugins` (where `libsrplg-*.so` live)
- `--plugin=ietf-system` (repeatable allowlist; empty = load all in dir)
- `--sysrepo-socket=…` (already there; affects SHM repo path)

At `server.New`, after `Connect`, if a plugins dir is configured confd
calls `pluginHost.Start(conn, specs)` **before** `ListenAndServe`. The
plugin host and the NETCONF server share the same `sr_conn_ctx_t` with
independent sessions.

## 7. `sysrepo-notifd` (RFC 8639)

`sysrepo-notifd` implements configured subscription delivery (RFC 8639)
— a UDP notification relay. It is **optional**:

- **v1 (default): leave external.** confd does not yet implement
  `<create-subscription>`; when it does, the notification relay can
  stay as a separate process or be absorbed.
- **v2 (build tag `notifd`): absorb into confd.** The notifd is also a
  `sr_connect` peer with its own event loop, same pattern as the plugin
  host. A `notifd` build tag compiles it in; otherwise confd runs
  without it.

Either way, notifd is orthogonal to the "single daemon" goal — it serves
a NETCONF feature confd doesn't implement yet.

## 8. YANG provisioning

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

## 9. Build & packaging

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

The `Makefile` gains:
```
make plugins     # cmake build of telekom/sysrepo-plugins -> build/plugins/*.so
make install     # go install confd + cp plugins to $(DESTDIR)/usr/lib/confd/plugins
```

## 10. Risks & mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| C++ plugin crashes (sdbus/libnl) abort the Go process | NETCONF server dies with the plugin | v1: accept (same as `sysrepo-plugind`); v2: fork+supervise per plugin |
| Thread-local state in sdbus-c++ vs Go's `LockOSThread` | subtle deadlocks | keep init/cleanup on locked threads; never call cgo from arbitrary goroutines |
| Plugin load order / inter-plugin dependencies | init failures | ordered manifest; reverse-order cleanup; per-plugin disable on init error |
| YANG module version skew between confd's goyang cache and sysrepo SHM | capability drift | confd loads its goyang cache **from sysrepo's installed modules** (already the design), so they can't diverge |
| dlopen + static libsysrepo symbol clashes | duplicate `sr_*` symbols | link libsysrepo **once** (in confd); plugins link it dynamically; verify with `ldd`/`nm` in CI |
| SIGPIPE from a half-closed SSH channel | crash | Go runtime ignores SIGPIPE on non-stdout writes; verify, add `signal.Ignore(syscall.SIGPIPE)` |
| First `sr_connect` initializes SHM; race with a concurrent `sysrepo-plugind` | double-init | don't run `sysrepo-plugind` alongside confd; confd is its replacement. Document this. |
| SHM lifecycle: confd crash leaves stale SHM | next restart re-attaches | sysrepo already handles this (`sr_connect` re-creates/attaches); no new risk vs. today |

## 11. Phasing

| Phase | Scope | Exit criteria |
|---|---|---|
| **P1** | cgo datastore adapter real (`sr_connect`, `sr_session_*`, `sr_get_items`, `sr_lock`) | confd reads live sysrepo running/operational data over SSH; Netopeer2 conformance for `<get>`/`<get-config>` |
| **P2** | plugin host: `dlopen` + per-plugin session + init/cleanup + ordered shutdown | one Telekom plugin (`ietf-system`) loaded by confd; `<get>` returns real hostname/timezone from the plugin |
| **P3** | multi-plugin, manifest, `--plugins-dir`/`--plugin`, YANG self-provisioning | all 7 Telekom plugins load; `systemctl start confd` is the only step |
| **P4** | (optional) absorb `sysrepo-notifd` behind `notifd` build tag | RFC 8639 configured subscriptions work without a separate process |
| **P5** | (optional) per-plugin fork+supervise hardening | a crashing plugin no longer takes down NETCONF |

## 12. Why this shape

- **There is no `sysrepod` to embed.** sysrepo is a SHM library; `sr_connect`
  opens the datastore. "Single process" just means confd is the only peer,
  not that we absorb a server.
- **confd replaces `sysrepo-plugind`** by hosting the plugins (`dlopen` +
  `init`/`cleanup`), and **replaces `netopeer2-server`** by serving NETCONF —
  both already-just-a-`sr_connect`-peer roles — in one Go process.
- The Telekom plugins are **reused verbatim** (no Go reimplementation); we
  inherit their RFC compliance and OS integration (netlink, sdbus, lm-sensors)
  for free.
- The plugin contract (`init`/`cleanup` + self-managed loop) maps cleanly
  onto Go's cgo + `LockOSThread`, so the host is a small (~300 LoC) layer.
- The `sysrepoadapter` interface from `DESIGN.md` is unchanged; only the
  `CGo` implementation gains a `pluginHost` field. Mock-based tests stay
  green without any plugin or sysrepo installed.
