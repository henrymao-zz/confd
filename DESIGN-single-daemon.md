# confd Single-Daemon Design: Embedding sysrepo + sysrepo-plugins

> Companion to `DESIGN.md`. Goal: make `confd` a **single daemon** that
> bundles three things that today run as separate processes — the
> **sysrepo datastore**, the **Telekom sysrepo-plugins**, and the
> **NETCONF server** — into one process so deployment is one binary,
> one lifecycle, one set of signals, one log stream.

## 1. What we are (and are not) bundling

| Component | Today | In `confd` | Notes |
|---|---|---|---|
| NETCONF server | `confd` (this repo) | ✅ already | unchanged |
| sysrepo **client lib** (libsysrepo) | linked by confd's cgo adapter | ✅ in-process via cgo | datastore access |
| sysrepo **plugins** (telekom/sysrepo-plugins) | `sysrepo-plugind` loads `libsrplg-*.so` | ✅ confd loads them | replaces `sysrepo-plugind` |
| sysrepo **datastore daemon** (`sysrepod`) | separate process | ⚠️ see §3 | optional v2, recommended-external v1 |

**Non-goals**
- Reimplementing any plugin in Go.
- Replacing libyang/libsysrepo with a pure-Go datastore.
- RESTCONF, Call-home (out of scope; orthogonal).

The Telekom plugins are C++20 (`sdbus-c++`, `libnl`, `libsystemd`, `umgmt`,
`libsensors`, …) and each has its own event loop (sdbus `IoContext`,
netlink sockets, etc.). The design **does not** port them to Go; it hosts
their compiled artifacts unchanged.

## 2. The key enabler: the plugin contract

Every Telekom plugin is built in two forms (see `main.c` of any plugin):

```
int  sr_plugin_init_cb   (sr_session_ctx_t *session, void **priv);
void sr_plugin_cleanup_cb(sr_session_ctx_t *session, void  *priv);
```

- The `libsrplg-<name>.so` artifact exports exactly these two symbols.
- `init` registers all sysrepo subscriptions (operational-data providers,
  change callbacks, RPC handlers) and **starts the plugin's own event loop
  on an internal thread**.
- `cleanup` joins that loop and unregisters.
- `sysrepo-plugind` itself does nothing more than `dlopen` each `.so`,
  call `init`, sleep, and call `cleanup` on SIGINT — see the standalone
  `main.c` in each plugin, which is a 40-line `connect → init → sleep →
  cleanup → disconnect` loop.

**Implication:** a Go host only has to own the **lifecycle**
(connect / per-plugin session / init / cleanup / shutdown). It does **not**
have to drive any plugin's event loop. This is what makes embedding in a
Go daemon tractable.

## 3. Where does `sysrepod` go?

sysrepo's datastore coordination lives in `sysrepod` (SHM, DS plugins,
notification store, locks). Two options, decided by a build tag:

### Option A — `sysrepod` stays external (v1, recommended)
- `confd` connects to a running `sysrepod` via libsysrepo IPC, exactly like
  Netopeer2 / sysrepo-plugind do today.
- `confd` *is* the `sysrepo-plugind` replacement (loads plugins) **and** the
  NETCONF server, but the datastore daemon is still its own process.
- This is still "one daemon" from the **management plane** perspective
  (the only process a user talks NETCONF to), and it's the safe default:
  no fork of sysrepo's SHM lifecycle, no surprises under load.
- **Deployment becomes 2 processes** (`sysrepod` + `confd`) instead of the
  current 4 (`sysrepod` + `sysrepo-plugind` + `netopeer2-server` + each
  standalone plugin binary).

### Option B — embed `sysrepod` into `confd` (v2, stretch)
- Take sysrepo's `sysrepod` `main()` and run it on a dedicated OS thread
  spawned from cgo, sharing one process. The datastore SHM files still
  exist on disk; only the coordinating daemon process is absorbed.
- Requires sysrepo to expose its daemon entry as a callable
  `sr_daemon_run(ctx)` with a stop fd. Upstream `sysrepod` is a normal
  `main()`; we'd add a small shim patch or fork.
- **True single process.** Worth it only when the deployment target
  strictly forbids helper processes (e.g. a signed monolithic appliance
  image). Not recommended for v1.

The rest of this doc assumes **Option A**; the plugin-host layer is
identical under Option B (only the connect target differs).

## 4. Architecture

```
                 ┌──────────────────────────── confd (one process) ────────────────────────────┐
                 │                                                                            │
  NETCONF ─────▶ │  transport ─▶ framing ─▶ rpc ─▶ operations ─▶ sysrepoadapter (cgo)         │
  (SSH)          │                                          │                                  │
                 │                                          │   sr_connect  ──┐                 │
                 │  ┌──────────────────────────────────────┐ │                 │                │
                 │  │ plugin host (cgo, replaces           │ │                 ▼                │
                 │  │ sysrepo-plugind)                    │ │          ┌───────────────┐        │
                 │  │  • dlopen libsrplg-ietf-system.so    │ │          │ libsysrepo    │        │
                 │  │  • dlopen libsrplg-ietf-ifaces.so    │ └── 1:1 ──▶│ (in-process)  │        │
                 │  │  • dlopen libsrplg-ietf-routing.so …  │            └──────┬────────┘        │
                 │  │  • per-plugin sr_session_start        │                   │ IPC/SHM         │
                 │  │  • sr_plugin_init_cb  (starts loop)  │                   ▼                 │
                 │  │  • on SIGTERM: cleanup in reverse    │            ┌───────────────┐       │
                 │  └──────────────────────────────────────┘            │   sysrepod    │       │
                 │                                                      │  (external)   │       │
                 │  Go runtime ──────────────────── runtime.LockOSThread│   for v1      │       │
                 └──────────────────────────────────────────────────────┴───────────────┴───────┘
```

Three cgo responsibilities, isolated in `internal/sysrepocgo`:
1. **Datastore client** — the existing `CGo` adapter (`sr_connect`,
   `sr_session_start`, `sr_get_items`, `sr_lock`, …).
2. **Plugin host** — new: `dlopen`, symbol lookup, per-plugin session,
   `init`/`cleanup` lifecycle. Replaces `sysrepo-plugind`.
3. **(v2) datastore daemon** — optional `sysrepod` embedding behind a tag.

## 5. Plugin host design

### 5.1 Loading strategy: `dlopen`, not static link

Static-linking the C++ plugins into a Go binary is fragile (C++ static
init order, duplicate `sdbus-c++` singletons, ODR violations across
plugins). Instead we **`dlopen` the shipped `libsrplg-*.so` artifacts**
the same way `sysrepo-plugind` does:

```go
// internal/pluginhost/host.go (Go side)
type Spec struct {
    Name string   // "ietf-system"
    Path string   // /usr/lib/confd/plugins/libsrplg-ietf-system.so
}
type Loaded struct {
    Spec
    handle  unsafe.Pointer // dlopen handle (opaque)
    init    func(sess, priv *unsafe.Pointer) C.int
    cleanup func(sess, priv unsafe.Pointer)
    priv    unsafe.Pointer
}
```

The cgo layer exposes:
```c
void *cf_dlopen(const char *path);          // returns handle or NULL
int   cf_call_init(void *h, sr_session_ctx_t *s, void **priv);
void  cf_call_cleanup(void *h, sr_session_ctx_t *s, void *priv);
void  cf_dlclose(void *h);
```

`cf_call_init` looks up `sr_plugin_init_cb` via `dlsym` and invokes it.

### 5.2 Session ownership

Two acceptable models; we pick **shared connection, per-plugin session**:

| Model | Pros | Cons | Decision |
|---|---|---|---|
| One session, all plugins share it | simplest | a plugin that stops the session breaks everyone; subscription cleanup messy | rejected |
| **One connection, one session per plugin** | isolation; matches `sysrepo-plugind` | more SHM sessions | **chosen** |
| One connection + one session, then `sr_session_dup` | middle ground | not needed for v1 | later |

So: `sr_connect` once → for each plugin `sr_session_start` → `init(sess,
&priv)` → keep `sess`+`priv` for cleanup.

### 5.3 Event-loop ownership

**The host owns none.** Each plugin's `init` starts its own loop
(sdbus `IoContext`, libnl readers, timer threads). The host only:

- Spawns **one locked OS thread per plugin's init call** so that cgo +
  any thread-local state (sdbus-c++ uses thread-local dispatchers) stays
  stable: `runtime.LockOSThread()` for the duration of `init`, then
  `UnlockOSThread`. The plugin keeps using its own internal threads
  afterwards.
- Does **not** run a `while(!exit) sleep(1)` loop. Go's main goroutine
  blocks on a shutdown context; plugins keep themselves alive via their
  own loops.

### 5.4 Shutdown ordering (critical)

```
SIGINT/SIGTERM
  → stop accepting NETCONF transports (drain in-flight RPCs with timeout)
  → for each plugin in REVERSE load order:
        cf_call_cleanup(handle, session, priv)   // joins plugin loop
        sr_session_stop(session)
  → sr_disconnect()
  → process exit
```

Rationale: in-flight RPCs may be served by plugin-provided operational
data; once NETCONF is drained it's safe to tear plugins down. Reverse
order handles plugins that depend on others (e.g. routing depends on
interfaces) cleanly.

### 5.5 Failure handling

- A plugin whose `init` returns non-zero is **disabled**, logged, and
  skipped; confd keeps starting the rest. `sysrepo-plugind` aborts on this;
  we improve on it.
- A plugin that crashes (SEGV in its loop) takes the whole process down
  in v1. v2 hardening: run each plugin in a forked child supervised by
  confd (see §9 risks) — but that re-introduces processes, so deferred.

## 6. Integration with the existing `sysrepoadapter`

`internal/sysrepoadapter/adapter.go`'s `CGo` struct becomes the host:

```
CGo
 ├── Connect(ctx)  → sr_connect, store conn, hand to plugin host
 ├── OpenSession   → sr_session_start (used by per-RPC operations)
 ├── pluginHost    → *pluginhost.Host (new field)
 └── Close()       → pluginHost.Stop() then sr_disconnect
```

`cmd/confd` gains:
- `--plugins-dir=/usr/lib/confd/plugins` (where `libsrplg-*.so` live)
- `--plugin=ietf-system` (repeatable; allowlist; empty = load all)
- `--sysrepo-socket=…` (already there)

At `server.New`, after `Connect`, if `--adapter=sysrepo` and a plugins dir
is configured, confd calls `pluginHost.Start(conn, specs)` before
`ListenAndServe`. The plugin host and the NETCONF server share the same
`libsysrepo` connection (different sessions).

## 7. YANG provisioning

sysrepo still needs the YANG modules installed in its datastore before
plugins can subscribe. We keep the Telekom `plugins/install_yang_modules.sh`
model but make confd **self-provision on first boot**:

- confd ships a manifest (`/etc/confd/plugins.yaml`) listing each enabled
  plugin and its YANG files + features to enable (mirrors the per-plugin
  `sysrepoctl -i … --enable-feature …` block in the Telekom README).
- On startup, confd queries `sr_get_module_list`; for any module in the
  manifest that is missing or lacks a required feature, confd shells out to
  `sysrepoctl` (or, in v2, uses the `sr_install_module` API directly) to
  install it.
- This is idempotent and matches what a packaging `%post` script would do,
  but it keeps "single daemon" honest: no separate setup service.

## 8. Build & packaging

The single-binary promise is honored at **runtime**, not at compile time:
the C++ plugins are still built by their own CMake (they need `sdbus-c++`,
`libnl`, etc.). The confd build produces:

```
confd                              # one Go binary (statically links libsysrepo if possible)
/usr/lib/confd/plugins/
  libsrplg-ietf-system.so          # from telekom/sysrepo-plugins build
  libsrplg-ietf-interfaces.so
  libsrplg-ietf-routing.so
  libsrplg-ietf-hardware.so
  libsrplg-os-metrics.so
  libsrplg-ietf-access-control-list.so
  libsrplg-ieee802-dot1q-bridge.so
```

Packaging options (pick per distro):
1. **One `.deb`/`.rpm`** that `Depends: sysrepod` and bundles the plugin
   `.so`s. Closest to "single daemon" from the operator's view: `apt
   install confd && systemctl start confd`.
2. **One OCI image**: `confd` + plugin `.so`s + `sysrepod` in one container
   (v2 with Option B), or `confd` + plugin `.so`s talking to a sidecar
   `sysrepod` (v1).

The Go `Makefile` gains:
```
make plugins     # cmake build of telekom/sysrepo-plugins -> build/plugins/*.so
make install     # go install confd + cp plugins to $(DESTDIR)/usr/lib/confd/plugins
```

## 9. Risks & mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| C++ plugin crashes (sdbus/libnl) abort the Go process | NETCONF server dies with the plugin | v1: accept; v2: fork+supervise per plugin (re-introduces helper procs, trade-off documented) |
| Thread-local state in sdbus-c++ vs Go's `LockOSThread` | subtle deadlocks | keep init/cleanup on locked threads; never call cgo from arbitrary goroutines for a plugin's callbacks |
| `sysrepod` still required for v1 | not truly single-process | documented; Option B is the path to remove it |
| Plugin load order / inter-plugin dependencies | init failures | ordered manifest; reverse-order cleanup; per-plugin disable on init error |
| YANG module version skew between confd's goyang cache and sysrepo | capability drift | confd loads its goyang cache **from sysrepo's installed modules** (already the design), so they can't diverge |
| dlopen + static libsysrepo symbol clashes | duplicate `sr_*` symbols | link libsysrepo **once** (in confd); plugins link it dynamically; verify with `ldd`/`nm` in CI |
| SIGPIPE from a half-closed SSH channel killing the process | crash | plugin `main.c` already does `signal(SIGPIPE, SIG_IGN)`; confd's Go runtime ignores SIGPIPE on non-stdout writes — verify, add `signal.Ignore(syscall.SIGPIPE)` |

## 10. Phasing

| Phase | Scope | Exit criteria |
|---|---|---|
| **P1** | cgo datastore adapter real (`sr_connect`, `sr_session_*`, `sr_get_items`, `sr_lock`) | confd reads live sysrepo running/operational data over SSH; Netopeer2 conformance for `<get>`/`<get-config>` |
| **P2** | plugin host: `dlopen` + per-plugin session + init/cleanup + ordered shutdown | one Telekom plugin (`ietf-system`) loaded by confd; `<get>` returns real hostname/timezone from the plugin |
| **P3** | multi-plugin, manifest, `--plugins-dir`/`--plugin`, self-provisioning | all 7 Telekom plugins load; `systemctl start confd` is the only step |
| **P4** | (optional) embed `sysrepod` (Option B) | true single-process image; no `sysrepod` unit |
| **P5** | (optional) per-plugin fork+supervise hardening | a crashing plugin no longer takes down NETCONF |

## 11. Why this shape

- It **reuses the Telekom plugins verbatim** (no Go reimplementation), so we
  inherit their RFC compliance and OS integration (netlink, systemd-resolved
  via sdbus, lm-sensors, …) for free.
- It **replaces `sysrepo-plugind` and `netopeer2-server`** with one Go
  process — the actual "single daemon" win — while keeping `sysrepod`
  external in v1 for datastore safety.
- The plugin contract (`init`/`cleanup` + self-managed loop) maps cleanly
  onto Go's cgo + `LockOSThread`, so the host is a small (~300 LoC) layer.
- The `sysrepoadapter` interface from `DESIGN.md` is unchanged; only the
  `CGo` implementation gains a `pluginHost` field. Mock-based tests stay
  green without any plugin or sysrepo installed.
