# confd

A Go-based NETCONF server backed by **sysrepo**, using **[goyang](https://github.com/openconfig/goyang)** as the YANG schema parser. It is a lighter, Go-native alternative to Netopeer2 for environments where you want the NETCONF protocol and the sysrepo datastore, but a Go service.

This repository implements the **MVP** described in [`../confd-design.md`](../confd-design.md): read-only retrieval of configuration and operational data.

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
| `<edit-config>` / NACM / notifications | 🚧 phase 2+ |

## Architecture

```
transport (SSH) ─▶ framing ─▶ hello ─▶ rpc dispatch ─▶ operations
                                                      │
                              schema.Cache (goyang) ◀┤
                                                      │
                              sysrepoadapter (Mock | CGo) ─▶ sysrepod
```

- **`internal/schema`** is the only package that imports goyang.
- **`internal/sysrepoadapter`** defines a cgo-free `Adapter`/`Session`/`DataNode` interface. Two implementations:
  - `Mock` — in-memory YANG-modeled tree; used by tests and as the default when libsysrepo headers aren't installed.
  - `CGo` — selected by the `sysrepo` build tag; a thin cgo shim over libsysrepo (compiles only when sysrepo headers are present).
- Everything else (`transport`, `framing`, `hello`, `rpc`, `operations`, `server`) is pure Go and fully testable without cgo.

## Build

```
make build           # pure Go (uses the Mock adapter)
make sysrepo         # cgo build against libsysrepo (needs sysrepo headers)
make test            # unit + integration tests
make test-race       # with the race detector
make vet
```

## Run

```
go build -o /tmp/confd ./cmd/confd
/tmp/confd serve --bind=127.0.0.1:830 --password=confd --yang-path=./yang
```

Subcommands:
- `confd serve` — start the NETCONF listener.
- `confd schema-list --yang-path=<dir>` — list loaded YANG modules (debug aid).

## Try it

With a running `confd serve`, from another host:

```
ssh -p 830 -s confd@127.0.0.1 netconf
# -> <hello .../> from the server, then send your own <hello>
# -> <rpc message-id="1"><get/></rpc>
```

`confd` advertises `urn:ietf:params:netconf:base:1.1` plus one capability URI per loaded YANG module.

## Repository layout

```
confd/
├── cmd/confd/             # entrypoint (serve, schema-list)
├── internal/
│   ├── config/            # flags + defaults
│   ├── transport/         # SSH + in-memory pipe transports
│   ├── framing/          # RFC 6242 chunked framing
│   ├── hello/            # <hello> + capability negotiation
│   ├── rpc/              # <rpc> parse, dispatch, <rpc-error>
│   ├── operations/       # get, get-config, get-schema, lock, unlock, sessions
│   ├── schema/           # goyang-backed cache (the only goyang importer)
│   ├── sysrepoadapter/   # Adapter interface + Mock + CGo (build tag)
│   ├── data/             # DataNode -> NETCONF XML encoder
│   └── server/           # wiring + ServeTransport / ListenAndServe
└── yang/confd-test.yang  # in-tree YANG module used by tests
```

## Wiring the real sysrepo backend

1. Install sysrepo + libyang headers (`apt install libsysrepo-dev libyang-dev`).
2. `make sysrepo` (or `go build -tags sysrepo`).
3. Run `confd serve --adapter=sysrepo --sysrepo-socket=/run/sysrepo/sysrepo.sock`.

The `CGo` adapter currently returns a clear error until the full cgo binding (sr_connect / sr_session_start / sr_get_items) is filled in; the interface it must satisfy is in `internal/sysrepoadapter/adapter.go`.
