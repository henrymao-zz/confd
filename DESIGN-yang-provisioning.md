# Design: YANG Provisioning

> Phase 3 of the confd roadmap. Eliminates the manual `sysrepoctl -i`
> step by having confd install YANG modules into sysrepo on startup,
> using the sysrepo C API (`sr_install_module`) and the plugin YANG
> directories shipped in the `src/sysrepo-plugins/` submodule.

## 1. The problem

Today, YANG modules must be installed into sysrepo **manually** before
confd or its plugins can use them:

```bash
cd src/sysrepo-plugins/plugins
./install_yang_modules.sh   # runs ~30 sysrepoctl -i commands
```

This script is a shell loop of `sysrepoctl -i` and `sysrepoctl -c`
(enable-feature) calls — one per YANG file and per feature. If an
operator forgets to run it, plugins fail at `sr_plugin_init_cb` because
sysrepo doesn't know the YANG modules.

Additionally, confd's goyang schema cache (loaded from `--yang-path`)
and sysrepo's installed modules are **two independent stores** that can
diverge:

| Situation | Symptom |
|---|---|
| YANG installed in sysrepo but not in `--yang-path` | Plugin works, but confd doesn't advertise the module in `<hello>` and `<get-schema>` returns "not found" |
| YANG in `--yang-path` but not installed in sysrepo | confd advertises it, but the plugin can't subscribe (sysrepo rejects the subscription) |
| Different revision of the same module in each store | `<get-schema>` returns one revision, sysrepo uses another — data validation mismatches |

## 2. The design

### 2.1 Source of truth: the plugin YANG directories

Each Telekom plugin ships its YANG files in `plugins/<name>/yang/`. These
are the authoritative source for what YANG modules that plugin needs.

confd already has the `src/sysrepo-plugins/` submodule. The YANG files
are available at known paths:

```
src/sysrepo-plugins/plugins/ietf-system-plugin/yang/ietf-system@2014-08-06.yang
src/sysrepo-plugins/plugins/ietf-interfaces-plugin/yang/ietf-interfaces@2018-02-20.yang
src/sysrepo-plugins/plugins/ietf-routing-plugin/yang/ietf-routing@2018-03-13.yang
...
```

### 2.2 Manifest

A manifest file (`/etc/confd/plugins.yaml`) declares which plugins are
enabled and which YANG modules + features they require. It mirrors the
`install_yang_modules.sh` script but in a structured format:

```yaml
# /etc/confd/plugins.yaml
plugins:
  - name: ietf-system
    yang_dir: /usr/lib/confd/yang/ietf-system
    modules:
      - iana-crypt-hash@2014-08-06.yang
      - ietf-system@2014-08-06.yang
    features:
      ietf-system:
        - timezone-name
        - ntp
        - authentication
        - local-users

  - name: ietf-interfaces
    yang_dir: /usr/lib/confd/yang/ietf-interfaces
    modules:
      - ietf-interfaces@2018-02-20.yang
      - iana-if-type@2017-01-19.yang
      - ietf-ip@2018-02-22.yang
      - ietf-if-extensions@2020-07-29.yang
      - ieee802-dot1q-types.yang
      - ietf-if-vlan-encapsulation@2020-07-13.yang
    features:
      ietf-interfaces:
        - if-mib
      ietf-if-extensions:
        - sub-interfaces

  - name: ietf-routing
    yang_dir: /usr/lib/confd/yang/ietf-routing
    modules:
      - ietf-routing@2018-03-13.yang
      - ietf-ipv4-unicast-routing@2018-03-13.yang
      - ietf-ipv6-unicast-routing@2018-03-13.yang
    import_search_dirs:
      - /usr/lib/confd/yang/ietf-routing

  # ... (ietf-hardware, ietf-access-control-list, ieee802-dot1q-bridge, os-metrics)
```

### 2.3 Provisioning flow (on startup)

```
confd startup
  │
  ├── 1. Read plugins.yaml (or auto-discover from --plugins-dir)
  │
  ├── 2. sr_connect() → sr_conn_ctx_t
  │
  ├── 3. For each plugin in the manifest:
  │     a. sr_get_module_info() → list of installed modules
  │     b. For each .yang file in the plugin's yang_dir:
  │        - If module is not installed → sr_install_module(conn, path, search_dirs, features)
  │        - If module is installed but missing a feature → sr_set_module_feature(conn, module, feature, SR_MOD_FEATURE_ENABLE)
  │
  ├── 4. Load goyang cache from the same yang_dir (so goyang and sysrepo agree)
  │
  ├── 5. Start plugins (dlopen → sr_plugin_init_cb)
  │
  └── 6. Start NETCONF listener
```

Step 3 is idempotent: if a module is already installed and all features are
enabled, `sr_install_module` returns `SR_ERR_OK` (or `SR_ERR_EXISTS` which
we treat as success). If a feature is already enabled, `sr_set_module_feature`
is a no-op.

### 2.4 New `Provisioner` type

```go
// internal/yangprov/provisioner.go

type Provisioner struct {
    conn      sysrepoadapter.Conn  // sr_conn_ctx_t
    yangDirs  []string              // search dirs for imports
}

type PluginSpec struct {
    Name      string
    YangDir   string
    Modules   []string              // .yang file names
    Features  map[string][]string   // module → feature names
    ImportDirs []string            // extra search dirs for sr_install_module
}

// Provision checks each plugin's YANG modules against sysrepo's installed
// modules and installs any that are missing.
func (p *Provisioner) Provision(specs []PluginSpec) error {
    // 1. sr_get_module_info → list of installed module names
    // 2. For each spec:
    //    For each module file:
    //      path := filepath.Join(spec.YangDir, moduleFile)
    //      if not installed → sr_install_module(conn, path, searchDirs, features)
    //      for module, features := range spec.Features:
    //        for each feature → sr_set_module_feature(conn, module, feature, SR_MOD_FEATURE_ENABLE)
}

// LoadCache loads the same YANG files into the goyang Cache so that
// <hello> capabilities and <get-schema> match what's in sysrepo.
func (p *Provisioner) LoadCache(cache *schema.Cache, specs []PluginSpec) error {
    for _, spec := range specs {
        cache.LoadDirectory(spec.YangDir)
    }
}
```

### 2.5 sysrepoadapter extension

The `Conn` interface gains two new methods:

```go
type Conn interface {
    // ... existing methods ...

    // GetModuleInfo returns the list of YANG modules installed in sysrepo.
    GetModuleInfo(ctx context.Context) ([]ModuleInfo, error)

    // InstallModule installs a YANG module into sysrepo.
    // searchDirs is a colon-separated list of import search directories.
    // features is a NULL-terminated list of feature names to enable.
    InstallModule(ctx context.Context, path, searchDirs string, features []string) error

    // SetModuleFeature enables or disables a feature on an installed module.
    SetModuleFeature(ctx context.Context, module, feature string, enable bool) error
}
```

#### Mock implementation

```go
func (c *mockConn) GetModuleInfo(ctx context.Context) ([]ModuleInfo, error) {
    // Return the modules list from the mock's modules field.
}

func (c *mockConn) InstallModule(ctx context.Context, path, searchDirs string, features []string) error {
    // Record the module name as installed (add to modules list).
    // Parse the .yang file with goyang to get the module name.
    return nil // success
}

func (c *mockConn) SetModuleFeature(ctx context.Context, module, feature string, enable bool) error {
    // No-op (mock doesn't enforce features).
    return nil
}
```

#### CGo implementation (behind `sysrepo` build tag)

```go
func (c *cgoConn) GetModuleInfo(ctx context.Context) ([]ModuleInfo, error) {
    // sr_get_module_info(conn, &data) → lyd_node tree
    // Parse the tree to extract module names + revisions
}

func (c *cgoConn) InstallModule(ctx context.Context, path, searchDirs string, features []string) error {
    // cPath := C.CString(path)
    // cSearchDirs := C.CString(searchDirs)  // or NULL
    // cFeatures := stringSliceToCStringArray(features)  // NULL-terminated
    // rc := C.sr_install_module(c.raw, cPath, cSearchDirs, cFeatures)
    // free, check rc
}

func (c *cgoConn) SetModuleFeature(ctx context.Context, module, feature string, enable bool) error {
    // sr_set_module_feature doesn't exist in sysrepo.h. The correct API is:
    // sr_set_module_replay_support or module-specific feature enable.
    // Actually, features are enabled at install time via sr_install_module's
    // features parameter. For already-installed modules, use sr_install_module
    // again (sysrepo treats re-install as no-op if the module is already
    // installed with the same features).
}
```

### 2.6 Auto-discovery (no manifest)

If no `plugins.yaml` is provided, confd can auto-discover YANG modules from
the `--plugins-dir`:

```go
func autoDiscoverYangDirs(pluginsDir string) []PluginSpec {
    // Walk pluginsDir/*/yang/ and collect all .yang files.
    // Group by plugin directory.
    // No features (features require per-plugin knowledge that only the
    // manifest can provide).
}
```

This is a best-effort mode: it installs all YANG modules but doesn't enable
features (e.g., `timezone-name` for `ietf-system`). The manifest is the
recommended way; auto-discovery is a fallback for simple setups.

### 2.7 Integration with server startup

```go
// in internal/server/server.go, New():

// After Connect, before plugin host start:
if cfg.YangProvisioner != nil {
    if err := cfg.YangProvisioner.Provision(cfg.PluginSpecs); err != nil {
        slog.Warn("server: YANG provisioning failed", "error", err)
    }
}
// Then load the goyang cache from the same YANG directories:
for _, spec := range cfg.PluginSpecs {
    cache.LoadDirectory(spec.YangDir)
}
// Then start plugins:
if ph != nil && len(cfg.PluginSpecs) > 0 {
    ph.Start(conn, cfg.PluginSpecs)
}
```

### 2.8 Config changes

```go
// in internal/config/config.go:
type Config struct {
    // ... existing fields ...
    YangProvisionerFile string   // path to plugins.yaml (default: /etc/confd/plugins.yaml)
    // If empty, auto-discover from --plugins-dir.
}
```

```bash
# CLI:
confd serve --plugins-dir=/usr/lib/confd/plugins \
            --yang-manifest=/etc/confd/plugins.yaml \
            --adapter=sysrepo
```

### 2.9 What this eliminates

Before:
```
sysrepoctl -i ietf-system-plugin/yang/iana-crypt-hash@2014-08-06.yang
sysrepoctl -i ietf-system-plugin/yang/ietf-system@2014-08-06.yang
sysrepoctl -c ietf-system -e timezone-name -e ntp -e authentication -e local-users
sysrepoctl -i ietf-interfaces-plugin/yang/ietf-interfaces@2018-02-20.yang
sysrepoctl -i ietf-interfaces-plugin/yang/iana-if-type@2017-01-19.yang
# ... 30 more lines
confd serve --yang-path=/usr/share/yang/modules
```

After:
```bash
confd serve --plugins-dir=/usr/lib/confd/plugins \
            --yang-manifest=/etc/confd/plugins.yaml \
            --adapter=sysrepo
# confd installs missing YANG modules, enables features, loads goyang cache,
# starts plugins, serves NETCONF — all in one command.
```

## 3. File changes

| File | Change |
|---|---|
| `internal/yangprov/provisioner.go` | New: `Provisioner`, `PluginSpec`, `Provision()`, `LoadCache()` |
| `internal/sysrepoadapter/adapter.go` | Add `GetModuleInfo`, `InstallModule`, `SetModuleFeature` to `Conn` |
| `internal/sysrepoadapter/mock.go` | Implement the 3 new methods on `mockConn` |
| `internal/sysrepoadapter/cgo_stub.go` | Implement the 3 new methods via `sr_get_module_info`, `sr_install_module` |
| `internal/config/config.go` | Add `YangProvisionerFile` field + `--yang-manifest` flag |
| `cmd/confd/main.go` | Read manifest, create `Provisioner`, call `Provision()` before plugin host start |
| `internal/server/server.go` | Call `Provisioner.Provision()` after `Connect()`, before `LoadDirectory` |
| `etc/confd/plugins.yaml` | New: the manifest file (installed by `make install`) |
| `Makefile` | `install` target copies `plugins.yaml` to `$(DESTDIR)/etc/confd/` |

## 4. Risks

| Risk | Mitigation |
|---|---|
| `sr_install_module` requires CONTEXT WRITE LOCK | `Provision()` runs before any NETCONF sessions or plugins start; no contention |
| Module already installed with different features | `sr_install_module` on an already-installed module is a no-op; features are enabled separately |
| Import resolution (module A imports module B) | `search_dirs` parameter in `sr_install_module` handles this; the manifest's `import_search_dirs` provides the directories |
| goyang vs libyang parse differences | goyang and libyang parse YANG differently; the same `.yang` file may produce different `*yang.Entry` trees. We load the same files into both, so the YANG text is identical — only the parsed representation might differ. For capability advertisement and `<get-schema>`, goyang is the source; for data validation, libyang (sysrepo) is the source. They can't disagree on the text, only on the interpretation. |
| Plugin YANG files not shipped with confd | `make install` copies them from `src/sysrepo-plugins/plugins/*/yang/` to `$(DESTDIR)/usr/lib/confd/yang/<plugin>/` |
