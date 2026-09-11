# Design: Consolidate Configuration into a Single YAML File

> Status: **proposal** — not yet implemented.

## Problem

Currently confd's configuration is spread across multiple CLI flags
(`--bind`, `--password`, `--adapter`, `--plugins-dir`, `--plugin`,
`--yang-manifest`, `--sysrepo-socket`) with no way to persist them in a
file. The `--yang-manifest` points to a `plugins.yaml` that contains
YANG provisioning info but not the rest of confd's config. This makes
deployment harder: the operator needs a systemd unit file with a long
flag list, and the plugin specs and YANG manifest are in a separate file.

## Proposal

### Single `confd.yaml` file

All configuration in one YAML file (`/etc/confd/confd.yaml` by default,
overridable with `--config=<path>`):

```yaml
# confd configuration file

ssh:
  bind: "0.0.0.0:830"
  host_key: ""
  password: ""

adapter: sysrepo
sysrepo_socket: ""

yang:
  manifest: ""              # path to plugins.yaml (empty = no provisioning)
  # If manifest is empty, these dirs are loaded directly (for tests):
  # dirs:
  #   - /usr/share/yang/modules

plugins:
  dir: "/usr/lib/confd/plugins"
  names:
    - ietf-system
    - ietf-interfaces
```

### CLI flags override YAML

Every field has a CLI flag override:
- `--config=<path>` — path to the YAML file (default `/etc/confd/confd.yaml`)
- `--bind=<addr>` — overrides `ssh.bind`
- `--host-key=<path>` — overrides `ssh.host_key`
- `--password=<pw>` — overrides `ssh.password`
- `--adapter=<mock|sysrepo>` — overrides `adapter`
- `--sysrepo-socket=<path>` — overrides `sysrepo_socket`
- `--yang-manifest=<path>` — overrides `yang.manifest`
- `--plugins-dir=<dir>` — overrides `plugins.dir`
- `--plugin=<name>` — appends to `plugins.names`

### Merging logic

1. Start with `Default()` (compiled-in defaults).
2. If `--config=<path>` is set (or the default `/etc/confd/confd.yaml` exists),
   load the YAML file and merge it into the config.
3. Apply CLI flag overrides on top of the YAML + defaults.

### What changes

| Field | Before | After |
|---|---|---|
| `SSHBind` | `--bind` flag only | `ssh.bind` in YAML + `--bind` override |
| `SSHHostKey` | `--host-key` flag only | `ssh.host_key` in YAML + `--host-key` override |
| `SSHPassword` | `--password` flag only | `ssh.password` in YAML + `--password` override |
| `Adapter` | `--adapter` flag only | `adapter` in YAML + `--adapter` override |
| `SysrepoSocket` | `--sysrepo-socket` flag only | `sysrepo_socket` in YAML + `--sysrepo-socket` override |
| `YangManifest` | `--yang-manifest` flag only | `yang.manifest` in YAML + `--yang-manifest` override |
| `PluginsDir` | `--plugins-dir` flag only | `plugins.dir` in YAML + `--plugins-dir` override |
| `Plugins` | `--plugin` flags only | `plugins.names` in YAML + `--plugin` overrides (append) |

### Generated default file

A default `confd.yaml` is generated in the repo at `confd.yaml` with
comments explaining each field. `make install` copies it to
`$(DESTDIR)/etc/confd/confd.yaml`.

### File changes

| File | Change |
|---|---|
| `internal/config/config.go` | Add `ConfigYAML` struct, `LoadConfig(path)` function, merge logic |
| `cmd/confd/main.go` | Add `--config=<path>` flag; call `LoadConfig` before `FromFlags` |
| `confd.yaml` | New: default config file with comments |
| `Makefile` | `install` copies `confd.yaml` to `$(DESTDIR)/etc/confd/` |
| `internal/yangprov/provisioner.go` | No change (already uses `PluginSpec` from YAML) |
