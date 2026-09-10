# confd

A Go-based NETCONF server backed by **sysrepo**, using **[goyang](https://github.com/openconfig/goyang)** as the YANG schema parser. It is a lighter, Go-native alternative to Netopeer2 that also replaces `sysrepo-plugind` by hosting Telekom sysrepo-plugins in a single process.

## Status

| Capability | Status |
|---|---|
| NETCONF over SSH (RFC 6242) | ✅ |
| Chunked framing + base:1.0/1.1 negotiation | ✅ |
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

1. **NETCONF server** — SSH transport, RFC 6242 framing, `<hello>` capability negotiation, `<rpc>` dispatch, and the protocol operation handlers (`<get>`, `<get-config>`, `<get-schema>`, `<lock>`, `<close-session>`, `<kill-session>`).
2. **sysrepo datastore peer** — calls `sr_connect()` to open the shared-memory datastore; each NETCONF session gets its own `sr_session_ctx_t`.
3. **Plugin host** (replaces `sysrepo-plugind`) — `dlopen`s the shipped `libsrplg-*.so` artifacts, gives each a `sr_session_start`, calls `sr_plugin_init_cb` (which starts the plugin's own event loop), and on shutdown calls `sr_plugin_cleanup_cb` in reverse load order.

sysrepo is a **shared-memory library**, not a client-server architecture — there is no `sysrepod` datastore server process. `sr_connect()` opens SHM files; coordination is via mutexes. So "single daemon" means confd is the only `sr_connect` peer, not that we embed a server.

See [`DESIGN.md`](DESIGN.md) for the full architecture and design rationale.

## Architecture

```
transport (SSH) ─▶ framing ─▶ hello ─▶ rpc dispatch ─▶ operations ─▶ sysrepoadapter (cgo)
                                                       │
                               schema.Cache (goyang) ◀─┤
                                                       │
                               pluginhost (dlopen) ◀───┤  replaces sysrepo-plugind
                                                       │
                               libsysrepo (SHM peer) ◀─┘
```

- **`internal/schema`** — the only package that imports goyang (schema cache, capabilities, `get-schema`).
- **`internal/sysrepoadapter`** — cgo-free `Adapter`/`Session`/`DataNode` interface; `Mock` (pure Go) or `CGo` (behind `sysrepo` build tag).
- **`internal/pluginhost`** — replaces `sysrepo-plugind`; `NoopHost` (default), `MockHost` (tests), or `CGoHost` (behind `sysrepo` build tag, uses `dlopen`).
- Everything else (`transport`, `framing`, `hello`, `rpc`, `operations`, `server`) is pure Go and fully testable without cgo.

## Build

```
make build           # pure Go (uses the Mock adapter, NoopHost)
make sysrepo         # cgo build against libsysrepo (needs sysrepo headers + dlopen)
make test            # unit + integration tests
make test-race       # with the race detector
make vet
make plugins         # cmake build of telekom/sysrepo-plugins -> build/plugins/*.so
make install         # go install confd + cp plugins to $(DESTDIR)/usr/lib/confd/plugins
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
│   ├── transport/         # SSH + in-memory pipe transports
│   ├── framing/           # RFC 6242 chunked framing
│   ├── hello/             # <hello> + capability negotiation
│   ├── rpc/               # <rpc> parse, dispatch, <rpc-error>
│   ├── operations/        # get, get-config, get-schema, lock, unlock, sessions
│   ├── schema/            # goyang-backed cache (the only goyang importer)
│   ├── sysrepoadapter/    # Adapter interface + Mock + CGo (build tag)
│   ├── pluginhost/        # replaces sysrepo-plugind (dlopen, build tag)
│   ├── data/              # DataNode -> NETCONF XML encoder
│   └── server/            # wiring + ServeTransport / ListenAndServe
├── yang/confd-test.yang   # in-tree YANG module used by tests
├── DESIGN.md              # full architecture & design document
├── Makefile
└── README.md
```

## Wiring the real sysrepo backend + plugins

1. Install sysrepo + libyang headers (`apt install libsysrepo-dev libyang-dev`).
2. Build the Telekom plugins: `git clone https://github.com/telekom/sysrepo-plugins && cd sysrepo-plugins && mkdir build && cd build && cmake .. && make -j`.
3. `make sysrepo` (or `go build -tags sysrepo`).
4. Run `confd serve --adapter=sysrepo --plugins-dir=/usr/lib/confd/plugins`.

The `CGo` adapter uses `sr_connect`, `sr_session_start`, `sr_session_switch_ds`, `sr_get_items`, `sr_lock`/`sr_unlock`, and `sr_disconnect`. The `CGoHost` uses `dlopen` + `sr_plugin_init_cb` / `sr_plugin_cleanup_cb`. Both share one `sr_conn_ctx_t` with independent `sr_session_ctx_t`s.

## Testing

All 12 packages pass `go test -race` without any sysrepo or cgo installed — the `Mock` adapter and `MockHost` provide in-memory implementations for tests. A real SSH end-to-end test (`TestServer_SSHEndToEnd`) dials the server over loopback and exercises the full `<hello>` + `<get-config>` + `<get>` flow with `golang.org/x/crypto/ssh`.

Verified end-to-end in a multipass VM (Ubuntu 22.04) with a Python NETCONF client: `<hello>`, `<get-config>`, `<get>`, `<get-schema>`, `<edit-config>` (rpc-error), and `<close-session>` all work correctly over SSH with base:1.1 chunked framing.
