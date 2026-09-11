# confd

A Go-based NETCONF server backed by **sysrepo**, using **[goyang](https://github.com/openconfig/goyang)** as the YANG schema parser and **[nemith.io/netconf](https://github.com/nemith/netconf)** for the NETCONF protocol layer. It is a lighter, Go-native alternative to Netopeer2 that also replaces `sysrepo-plugind` by hosting Telekom sysrepo-plugins in a single process.

## Status

| Capability | Status |
|---|---|
| NETCONF over SSH (RFC 6242) | ✅ |
| Chunked framing + base:1.0/1.1 negotiation (via nemith) | ✅ |
| `<get>` (operational) | ✅ |
| `<get-config>` (running/startup/candidate) | ✅ |
| `<get-schema>` (RFC 6022) | ✅ |
| Subtree + XPath filters (simplified) | ✅ |
| `<lock>` / `<unlock>` | ✅ |
| `<close-session>` / `<kill-session>` | ✅ |
| SSH `subsystem` request handling | ✅ |
| Plugin host (replaces `sysrepo-plugind`) | ✅ (behind `sysrepo` build tag) |
| `<edit-config>` / NACM / notifications | 🚧 phase 2+ |

## How it works

confd is a **single daemon** that bundles three roles into one Go process:

1. **NETCONF server** — SSH transport, RFC 6242 framing (nemith `transport.Framer`), `<hello>` capability negotiation (nemith `Hello`/`CapabilitySet`), `<rpc>` dispatch (`transport.ServerLoop`), and the protocol operation handlers (`<get>`, `<get-config>`, `<get-schema>`, `<lock>`, `<close-session>`, `<kill-session>`).
2. **sysrepo datastore peer** — calls `sr_connect()` to open the shared-memory datastore; each NETCONF session gets its own `sr_session_ctx_t`.
3. **Plugin host** (replaces `sysrepo-plugind`) — `dlopen`s the shipped `libsrplg-*.so` artifacts, gives each a `sr_session_start`, calls `sr_plugin_init_cb` (which starts the plugin's own event loop), and on shutdown calls `sr_plugin_cleanup_cb` in reverse load order.

sysrepo is a **shared-memory library**, not a client-server architecture — there is no `sysrepod` datastore server process. `sr_connect()` opens SHM files; coordination is via mutexes. So "single daemon" means confd is the only `sr_connect` peer, not that we embed a server.

See [`DESIGN.md`](DESIGN.md) for the full architecture and design rationale.

## Architecture

```
SSH client ─▶ transport.NewSSH() ─▶ Transport (SSH channel)
                                       │
                           transport.NewNemithTransport() wraps with
                           nemith's transport.Framer
                                       │
                           transport.ServerLoop()
                             ├── <hello> exchange (netconf.Hello)
                             ├── base:1.1 negotiation (Framer.Upgrade)
                             └── <rpc> loop → handlers → <rpc-reply>
                                       │
                           operations.BuildHandlers()
                             └── bridge to rpc.Dispatcher (internal/rpc)
                                   └── operations (get, get-config, ...)
                                         ├── schema.Cache (goyang)
                                         └── sysrepoadapter (Mock | CGo)
```

* **`internal/transport`** — SSH listener + nemith framing + server-side dispatch loop (`ServerLoop`).
- **`internal/transport`** — SSH listener + channel + subsystem handling; wraps the SSH channel with nemith's `Framer` via `NewNemithTransport()`.
- **`internal/schema`** — the only package that imports goyang (schema cache, capabilities, `get-schema`).
- **`internal/sysrepoadapter`** — cgo-free `Adapter`/`Session`/`DataNode` interface; `Mock` (pure Go) or `CGo` (behind `sysrepo` build tag).
- **`internal/pluginhost`** — replaces `sysrepo-plugind`; `NoopHost` (default), `MockHost` (tests), or `CGoHost` (behind `sysrepo` build tag, uses `dlopen`).
- **`internal/data`** — `DataNode` → NETCONF XML encoder (no Go library does this).
- **`internal/rpc`** — bridge: operations use `rpc.Dispatcher`/`rpc.Context` internally; `operations.BuildHandlers()` wraps them as `transport.Handler`.
- Everything except `internal/sysrepoadapter` (cgo) and `internal/pluginhost` (cgo) is pure Go and fully testable without cgo.

## Build

### Quick start (tests only, no dependencies)

```
make test            # run tests with mock adapter (no cgo/sysrepo needed)
make test-race       # tests with the race detector
make vet
```

No system dependencies needed — the `Mock` adapter and `MockHost` provide in-memory implementations for all tests.

### Full build with sysrepo backend + plugins (Ubuntu 26.04)

#### 1. Install system dependencies

```
sudo apt install -y \
  golang-go \
  pkg-config \
  cmake g++ \
  libsysrepo-dev libyang-dev \
  libnl-3-dev libnl-route-3-dev libnl-genl-3-dev libnl-nf-3-dev \
  libsystemd-dev \
  libsdbus-c++-dev \
  libnftables-dev \
  libsensors-dev \
  libproc2-dev \
  nlohmann-json3-dev
```

#### 2. Initialize git submodules

All dependencies (libyang, sysrepo, libyang-cpp, sysrepo-cpp, umgmt, and the Telekom sysrepo-plugins) are included as git submodules:

```
git submodule update --init --recursive
```

Submodule layout:

| Submodule | Path | Version | Purpose |
|---|---|---|---|
| [CESNET/libyang](https://github.com/CESNET/libyang) | `src/libyang` | v5.8.6 | YANG data modeling (C library) |
| [sysrepo/sysrepo](https://github.com/sysrepo/sysrepo) | `src/sysrepo` | v5.1.0 | YANG-based datastore (C library) |
| [CESNET/libyang-cpp](https://github.com/CESNET/libyang-cpp) | `src/libyang-cpp` | pre-v6 | C++ bindings for libyang |
| [sysrepo/sysrepo-cpp](https://github.com/sysrepo/sysrepo-cpp) | `src/sysrepo-cpp` | pre-v6 | C++ bindings for sysrepo |
| [sartura/umgmt](https://github.com/sartura/umgmt) | `src/umgmt` | main | Userspace management library |
| [telekom/sysrepo-plugins](https://github.com/telekom/sysrepo-plugins) | `src/sysrepo-plugins` | main | Telekom sysrepo plugins |

#### 3. Build C++ dependencies (libyang, sysrepo, libyang-cpp, sysrepo-cpp, umgmt)

These libraries are not available as Ubuntu packages (or their packaged versions are too old) and must be built from the submodules:

```
make build-deps
sudo ldconfig
```

This builds (in order):
- **libyang** v5.8.6 — from `src/libyang`
- **sysrepo** v5.1.0 — from `src/sysrepo` (depends on libyang 5.x)
- **libyang-cpp** — from `src/libyang-cpp` (pinned to pre-v6 commit for libyang 5.x compatibility)
- **sysrepo-cpp** — from `src/sysrepo-cpp` (pinned to pre-v6 commit)
- **umgmt** — from `src/umgmt`

#### 4. Build the Telekom sysrepo-plugins

```
make plugins
```

This runs cmake on `./sysrepo-plugins` (with `-DSYSTEMD_IFINDEX=1`) and copies `libsrplg-*.so` files to `/usr/lib/confd/plugins/`. The following plugins are built:

| Plugin | YANG module | Description |
|---|---|---|
| `ietf-system` | `ietf-system` | System hostname, timezone, DNS, NTP, auth (RFC 7317) |
| `ietf-interfaces` | `ietf-interfaces` | Network interface management (RFC 7223) |
| `ietf-routing` | `ietf-routing` | Routing management (RFC 8022) |
| `ietf-hardware` | `ietf-hardware` | Hardware management (RFC 8348) |
| `ietf-access-control-list` | `ietf-access-control-list` | ACLs (RFC 8519) |
| `ieee802-dot1q-bridge` | `ieee802-dot1q-bridge` | 802.1Q bridge config (IEEE 802.1Q-2018) |
| `os-metrics` | `os-metrics` | OS-level metrics (Debian) |

#### 5. Build confd

```
make build           # builds with -tags sysrepo (cgo against libsysrepo)
```

### All Makefile targets

```
make build           # build confd with sysrepo cgo backend (default)
make test            # run tests with mock adapter (no cgo needed)
make test-race       # tests with the race detector
make vet             # go vet
make build-deps      # build libyang, sysrepo, libyang-cpp, sysrepo-cpp, umgmt from submodules
make plugins         # build telekom/sysrepo-plugins -> /usr/lib/confd/plugins/*.so
make install         # install confd binary + plugin .so files
make clean           # remove build artifacts
```
make cover           # test coverage report
make install         # install confd binary + plugin .so files
make clean           # remove build artifacts
```

## Run

```
go build -o /tmp/confd ./cmd/confd
/tmp/confd serve --bind=127.0.0.1:830 --password=confd --yang-path=./yang
```

With plugins (single-daemon mode, requires `-tags sysrepo`):
```
/tmp/confd serve --bind=0.0.0.0:830 --password=confd --adapter=sysrepo \
  --yang-path=/usr/share/yang/modules \
  --plugins-dir=/usr/lib/confd/plugins \
  --plugin=ietf-system --plugin=ietf-interfaces
```

Subcommands:
- `confd serve` — start the NETCONF listener.
- `confd schema-list --yang-path=<dir>` — list loaded YANG modules (debug aid).

## Try it

With a running `confd serve`, from another host:

```
ssh -p 830 -s confd@127.0.0.1 netconf
# -> <hello .../> from the server, then send your own <hello>
# -> <rpc message-id="1"><get-config><source><running/></source></get-config></rpc>
```

`confd` advertises `urn:ietf:params:netconf:base:1.1` plus one capability URI per loaded YANG module. It handles the SSH `subsystem` request that `ssh -s ... netconf` sends.

## Repository layout

```
confd/
├── cmd/confd/             # entrypoint (serve, schema-list)
├── internal/
│   ├── config/            # flags + defaults
│   ├── transport/         # SSH listener + channel + subsystem handling

│   ├── rpc/               # <rpc> dispatch, <rpc-error> (bridge layer)
│   ├── operations/        # get, get-config, get-schema, lock, unlock, sessions
│   ├── schema/            # goyang-backed cache (the only goyang importer)
│   ├── sysrepoadapter/    # Adapter interface + Mock + CGo (build tag)
│   ├── pluginhost/        # replaces sysrepo-plugind (dlopen, build tag)
│   ├── data/              # DataNode -> NETCONF XML encoder
│   └── server/            # wiring + ServeTransport / ListenAndServe
├── src/                   # git submodules: C/C++ dependencies + plugins
│   ├── libyang/           # CESNET/libyang v5.8.6
│   ├── sysrepo/           # sysrepo/sysrepo v5.1.0
│   ├── libyang-cpp/       # CESNET/libyang-cpp (pinned pre-v6)
│   ├── sysrepo-cpp/       # sysrepo/sysrepo-cpp (pinned pre-v6)
│   ├── umgmt/             # sartura/umgmt
│   └── sysrepo-plugins/   # telekom/sysrepo-plugins
├── tools/                 # netconf_ssh.py — paramiko-based test client
├── yang/confd-test.yang   # in-tree YANG module used by tests
├── DESIGN.md              # full architecture & design document
├── Makefile
└── README.md
```

## Wiring the real sysrepo backend + plugins

1. Install system dependencies (see [Build](#build) above).
2. `git submodule update --init` — fetch the Telekom sysrepo-plugins source.
3. `make build-deps && sudo ldconfig` — build libyang-cpp, sysrepo-cpp, umgmt.
4. `make plugins` — build the plugin `.so` files and install to `/usr/lib/confd/plugins/`.
5. `make build` — build confd with cgo against libsysrepo.
6. Run `confd serve --adapter=sysrepo --plugins-dir=/usr/lib/confd/plugins`.

The `CGo` adapter uses `sr_connect`, `sr_session_start`, `sr_session_switch_ds`, `sr_get_items`, `sr_lock`/`sr_unlock`, and `sr_disconnect`. The `CGoHost` uses `dlopen` + `sr_plugin_init_cb` / `sr_plugin_cleanup_cb`. Both share one `sr_conn_ctx_t` with independent `sr_session_ctx_t`s.

## Testing

All 11 packages pass `go test -race` without any sysrepo or cgo installed — the `Mock` adapter and `MockHost` provide in-memory implementations for tests. A real SSH end-to-end test (`TestServer_SSHEndToEnd`) dials the server over loopback and exercises the full `<hello>` + `<get-config>` + `<get>` flow using nemith's framer on both sides.

Verified end-to-end in a multipass VM (Ubuntu 26.04 LTS) with a Python NETCONF client: `<hello>`, `<get-config>`, `<get>`, `<get-schema>`, `<edit-config>` (rpc-error), and `<close-session>` all work correctly over SSH with base:1.1 chunked framing. Use `tools/netconf_ssh.py` to reproduce:

```
python3 tools/netconf_ssh.py <vm-ip> 1830 confd
```
