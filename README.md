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
| `<edit-config>` (merge/replace/none) | ✅ |
| `<copy-config>` (datastore→datastore, config→datastore) | ✅ |
| `<delete-config>` (startup only) | ✅ |
| `<commit>` / `<discard-changes>` (candidate→running) | ✅ |
| `<validate>` | ✅ |
| YANG provisioning (auto-install into sysrepo) | ✅ |
| Plugin host (replaces `sysrepo-plugind`) | ✅ (behind `sysrepo` build tag) |
| NACM / notifications | 🚧 future |

## How it works

confd is a **single daemon** that bundles three roles into one Go process:

1. **NETCONF server** — SSH transport, RFC 6242 framing (nemith `transport.Framer`), `<hello>` capability negotiation (nemith `Hello`/`CapabilitySet`), `<rpc>` dispatch (`transport.ServerLoop`), and the protocol operation handlers (`<get>`, `<get-config>`, `<get-schema>`, `<edit-config>`, `<copy-config>`, `<delete-config>`, `<commit>`, `<discard-changes>`, `<validate>`, `<lock>`, `<close-session>`, `<kill-session>`).
2. **sysrepo datastore peer** — calls `sr_connect()` to open the shared-memory datastore; each NETCONF session gets its own `sr_session_ctx_t`.
3. **Plugin host** (replaces `sysrepo-plugind`) — `dlopen`s the shipped `libsrplg-*.so` artifacts, gives each a `sr_session_start`, calls `sr_plugin_init_cb` (which starts the plugin's own event loop), and on shutdown calls `sr_plugin_cleanup_cb` in reverse load order.

sysrepo is a **shared-memory library**, not a client-server architecture — there is no `sysrepod` datastore server process. `sr_connect()` opens SHM files; coordination is via mutexes. So "single daemon" means confd is the only `sr_connect` peer, not that we embed a server.

See [`DESIGN.md`](DESIGN.md) for the full architecture and design rationale.

## Architecture

```
SSH client ─▶ transport.NewSSH() ─▶ Transport (SSH channel)
                                       │
                           transport.Session (nemith Framer)
                                       │
                           transport.ServerLoop()
                             ├── <hello> exchange (netconf.Hello)
                             ├── base:1.1 negotiation (Framer.Upgrade)
                             └── <rpc> loop → handlers → <rpc-reply>
                                       │
                           operations.BuildHandlers()
                             └── bridge to rpc.Dispatcher (internal/rpc)
                                   └── operations (get, get-config, edit-config, ...)
                                         ├── schema.Cache (goyang)
                                         ├── sysrepoadapter (Mock | CGo)
                                         └── yangprov (YANG provisioning)
```

- **`internal/transport`** — SSH listener + nemith framing + `ServerLoop` (server-side protocol loop).
- **`internal/operations`** — all NETCONF operation handlers (get, get-config, edit-config, copy-config, delete-config, commit, discard-changes, validate, lock, unlock, close-session, kill-session).
- **`internal/rpc`** — bridge: operations use `rpc.Dispatcher`/`rpc.Context` internally; `operations.BuildHandlers()` wraps them as `transport.Handler`.
- **`internal/yangprov`** — YANG module provisioning: `Provisioner` installs missing modules into sysrepo, loads the same dirs into the goyang cache.
- **`internal/schema`** — the only package that imports goyang (schema cache, capabilities, `get-schema`).
- **`internal/sysrepoadapter`** — cgo-free `Adapter`/`Session`/`DataNode` interface; `Mock` (pure Go) or `CGo` (behind `sysrepo` build tag).
- **`internal/pluginhost`** — replaces `sysrepo-plugind`; `NoopHost` (default), `MockHost` (tests), or `CGoHost` (behind `sysrepo` build tag, uses `dlopen`).
- **`internal/data`** — `DataNode` → NETCONF XML encoder + `DecodeData` (XML → DataNode for `<edit-config>`).
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
  golang-go cmake g++ pkg-config \
  libnl-3-dev libnl-route-3-dev libnl-genl-3-dev libnl-nf-3-dev \
  libsystemd-dev libsdbus-c++-dev libnftables-dev libsensors-dev \
  libproc2-dev nlohmann-json3-dev doctest-dev
```

#### 2. Initialize git submodules

All dependencies (libyang, sysrepo, libyang-cpp, sysrepo-cpp, umgmt, and the Telekom sysrepo-plugins) are included as git submodules under `src/`:

```
git submodule update --init --recursive
```

#### 3. Build C++ dependencies (libyang, sysrepo, libyang-cpp, sysrepo-cpp, umgmt)

```
make build-deps
sudo ldconfig
```

#### 4. Build the Telekom sysrepo-plugins

```
make plugins
```

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

## Run

```
confd serve --bind=0.0.0.0:830 --password=confd --adapter=sysrepo \
  --yang-manifest=/etc/confd/plugins.yaml \
  --plugins-dir=/usr/lib/confd/plugins \
  --plugin=ietf-system --plugin=ietf-interfaces
```

`--yang-manifest` loads YANG modules into sysrepo (via `sr_install_module`) and into the goyang cache (for `<hello>` capabilities and `<get-schema>`). This replaces the manual `sysrepoctl -i` step and the old `--yang-path` flag.

Subcommands:
- `confd serve` — start the NETCONF listener.
- `confd schema-list --yang-manifest=<path>` — list loaded YANG modules (debug aid).

## Repository layout

```
confd/
├── cmd/confd/             # entrypoint (serve, schema-list)
├── internal/
│   ├── config/            # flags + defaults
│   ├── transport/         # SSH listener + nemith framing + ServerLoop
│   ├── rpc/               # <rpc> dispatch, <rpc-error> (bridge layer)
│   ├── operations/        # get, get-config, edit-config, copy-config, delete-config, commit, discard-changes, validate, lock, unlock, sessions
│   ├── schema/            # goyang-backed cache (the only goyang importer)
│   ├── sysrepoadapter/    # Adapter interface + Mock + CGo (build tag)
│   ├── pluginhost/        # replaces sysrepo-plugind (dlopen, build tag)
│   ├── yangprov/          # YANG provisioning (auto-install modules into sysrepo)
│   ├── data/              # DataNode → NETCONF XML encoder + decoder
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

## Testing

All 11 packages pass `go test -race` without any sysrepo or cgo installed — the `Mock` adapter and `MockHost` provide in-memory implementations for all tests. A real SSH end-to-end test (`TestServer_SSHEndToEnd`) dials the server over loopback and exercises the full `<hello>` + `<get-config>` + `<get>` flow using nemith's framer on both sides.

Verified end-to-end in a multipass VM (Ubuntu 26.04 LTS) with a Python NETCONF client: `<hello>`, `<get-config>`, `<get>`, `<get-schema>`, `<edit-config>` (rpc-error), and `<close-session>` all work correctly over SSH with base:1.1 chunked framing. Use `tools/netconf_ssh.py` to reproduce:

```
python3 tools/netconf_ssh.py <vm-ip> 1830 confd
```
