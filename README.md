# confd

A Go-based NETCONF server and CLI backed by **sysrepo**, using **[goyang](https://github.com/openconfig/goyang)** as the YANG schema parser and **[nemith.io/netconf](https://github.com/nemith/netconf)** for the NETCONF protocol layer. It is a lighter, Go-native alternative to Netopeer2 that also replaces `sysrepo-plugind` by hosting Telekom sysrepo-plugins in a single process, and includes an interactive CLI shell similar to `netopeer2-cli`.

## Status

| Capability | Status |
|---|---|
| NETCONF over SSH (RFC 6242) | ✅ |
| Chunked framing + base:1.0/1.1 negotiation (via nemith) | ✅ |
| `<get>` (operational) | ✅ |
| `<get-config>` (running/startup/candidate) | ✅ |
| `<get-schema>` (RFC 6022) | ✅ |
| Subtree + XPath filters | ✅ |
| `<edit-config>` (merge/replace/none) | ✅ |
| `<copy-config>` / `<delete-config>` | ✅ |
| `<commit>` / `<discard-changes>` / `<validate>` | ✅ |
| `<lock>` / `<unlock>` | ✅ |
| `<close-session>` / `<kill-session>` | ✅ |
| SSH `subsystem` request handling | ✅ |
| YANG provisioning (auto-install into sysrepo) | ✅ |
| Plugin host (replaces `sysrepo-plugind`) | ✅ (behind `sysrepo` build tag) |
| Interactive CLI shell (like `netopeer2-cli`) | ✅ |
| NACM / notifications | 🚧 future |

## How it works

confd has two modes:

1. **`confd serve`** — NETCONF server. SSH transport, RFC 6242 framing (nemith `transport.Framer`), `<hello>` capability negotiation, `<rpc>` dispatch, all RFC 6241 operations, YANG provisioning, and plugin host (replaces `sysrepo-plugind`).
2. **`confd`** (no subcommand) — Interactive CLI shell. Connects to a NETCONF server over SSH and provides a prompt for typing NETCONF operations, similar to `netopeer2-cli`.

sysrepo is a **shared-memory library**, not a client-server architecture — there is no `sysrepod` datastore server process. `sr_connect()` opens SHM files; coordination is via mutexes. So "single daemon" means confd is the only `sr_connect` peer, not that we embed a server.

See [`DESIGN.md`](DESIGN.md) for the full architecture and design rationale.

## Architecture

```
Server mode (confd serve):
  SSH client ─▶ transport.NewSSH() ─▶ Session (nemith Framer)
                                         │
                             transport.ServerLoop()
                               ├── <hello> exchange
                               ├── base:1.1 negotiation
                               └── <rpc> loop → handlers → <rpc-reply>
                                         │
                             operations.BuildHandlers()
                               └── operations (get, get-config, edit-config, ...)
                                     ├── schema.Cache (goyang)
                                     ├── sysrepoadapter (Mock | CGo)
                                     └── yangprov (YANG provisioning)

CLI mode (confd):
  confd> connect <host:port>
         │
     nemith client Session + transport/ssh.Dial
         │
  confd(1)> get-config --source running
         │
     Session.Do(rpc) → print reply
```

## Build

### Quick start (tests only, no dependencies)

```
make test            # run tests with mock adapter (no cgo/sysrepo needed)
```

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

```
git submodule update --init --recursive
```

#### 3. Build C++ dependencies + plugins

```
make configure
```

#### 4. Build confd

```
make build           # builds with -tags sysrepo (cgo against libsysrepo)
```

### All Makefile targets

```
make configure       # build all C++ deps + plugins from submodules
make build           # build confd with sysrepo cgo backend (depends on configure)
make test            # run tests with mock adapter (no cgo needed)
make run             # build + start the server
make install         # install confd binary + confd.yaml + plugin .so files
make clean           # remove build artifacts
```

## Run

### Server mode

```
confd serve --config /etc/confd/confd.yaml
```

Or with CLI flags overriding the YAML:

```
confd serve --bind=0.0.0.0:830 --password=confd --adapter=sysrepo \
  --plugins-dir=/usr/lib/confd/plugins \
  --plugin=ietf-system --plugin=ietf-interfaces
```

### CLI shell mode

```
$ confd
confd interactive NETCONF shell
confd> connect 127.0.0.1:830 --user confd --password confd
Session 1 established
confd(1)> get-config --source running
confd(1)> edit-config --target running --config '<system xmlns="..."><hostname>new</hostname></system>'
confd(1)> get-config --source running
confd(1)> quit
```

### Subcommands

- `confd` (no subcommand) — interactive CLI shell
- `confd serve [flags]` — start the NETCONF server
- `confd schema-list [flags]` — list loaded YANG modules

## Configuration

All configuration is in a single `/etc/confd/confd.yaml` file (overridable with `--config=<path>`):

```yaml
ssh:
  bind: "0.0.0.0:830"
  password: ""
adapter: sysrepo
plugins:
  dir: "/usr/lib/confd/plugins"
  entries:
    - name: ietf-system
      yang_dir: /usr/lib/confd/yang/ietf-system
      modules: [...]
      features: {...}
```

CLI flags override YAML values. See `confd.yaml` at the repo root for a complete example with all 7 Telekom sysrepo-plugins.

## Repository layout

```
confd/
├── cmd/confd/             # entrypoint (serve, schema-list, interactive shell)
├── internal/
│   ├── cli/               # interactive NETCONF client shell
│   ├── config/            # YAML config + CLI flags
│   ├── transport/         # SSH listener + nemith framing + ServerLoop
│   ├── rpc/               # <rpc> dispatch, <rpc-error> (bridge layer)
│   ├── operations/        # all NETCONF operation handlers
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
├── confd.yaml             # default config file with all plugins
├── DESIGN.md              # full architecture & design document
├── Makefile
└── README.md
```

## Testing

All 12 packages pass `go test -race` without any sysrepo or cgo installed — the `Mock` adapter and `MockHost` provide in-memory implementations for all tests. A real SSH end-to-end test (`TestServer_SSHEndToEnd`) dials the server over loopback and exercises the full `<hello>` + `<get-config>` + `<get>` flow using nemith's framer on both sides.

Verified end-to-end in a multipass VM (Ubuntu 26.04 LTS) with a Python NETCONF client: `<hello>`, `<get-config>`, `<get>`, `<get-schema>`, `<edit-config>` (rpc-error), and `<close-session>` all work correctly over SSH with base:1.1 chunked framing. Use `tools/netconf_ssh.py` to reproduce:

```
python3 tools/netconf_ssh.py <vm-ip> 1830 confd
```
