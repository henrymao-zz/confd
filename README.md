# confd

A Go-based NETCONF server and CLI backed by **sysrepo**, using **[goyang](https://github.com/openconfig/goyang)** as the YANG schema parser and **[nemith.io/netconf](https://github.com/nemith/netconf)** for the NETCONF protocol layer. It is a lighter, Go-native alternative to Netopeer2 that also replaces `sysrepo-plugind` by hosting Telekom sysrepo-plugins in a single process, and includes an interactive CLI shell similar to `netopeer2-cli`.

## How it works

confd has two modes:

1. **`confd serve`** — NETCONF server. SSH transport, RFC 6242 framing (nemith `transport.Framer`), `<hello>` capability negotiation, `<rpc>` dispatch, all RFC 6241 operations, YANG provisioning, and plugin host (replaces `sysrepo-plugind`).
2. **`confd`** (no subcommand) — Interactive CLI shell. Connects to a NETCONF server over SSH and provides a prompt for typing NETCONF operations, similar to `netopeer2-cli`.

See [`DESIGN.md`](DESIGN.md) for the full architecture and design rationale.

## Build

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


## Testing

All 12 packages pass `go test -race` without any sysrepo or cgo installed — the `Mock` adapter and `MockHost` provide in-memory implementations for all tests. A real SSH end-to-end test (`TestServer_SSHEndToEnd`) dials the server over loopback and exercises the full `<hello>` + `<get-config>` + `<get>` flow using nemith's framer on both sides.

Verified end-to-end in a multipass VM (Ubuntu 26.04 LTS) with a Python NETCONF client: `<hello>`, `<get-config>`, `<get>`, `<get-schema>`, `<edit-config>` (rpc-error), and `<close-session>` all work correctly over SSH with base:1.1 chunked framing. Use `tools/netconf_ssh.py` to reproduce:

```
python3 tools/netconf_ssh.py <vm-ip> 1830 confd
```
