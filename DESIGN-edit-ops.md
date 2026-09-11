# Design: NETCONF Edit Operations (`<edit-config>`, `<copy-config>`, `<delete-config>`, `<commit>`, `<discard-changes>`, `<validate>`)

> Phase 2 of the confd roadmap. Adds write operations to the NETCONF
> server, backed by sysrepo's edit/commit/copy/validate API.

## 1. Scope

| Operation | RFC 6241 § | sysrepo API | NETCONF capability |
|---|---|---|---|
| `<edit-config>` | 7.2 | `sr_edit_batch` + `sr_apply_changes` | `:base:1.1` |
| `<copy-config>` | 7.3 | `sr_copy_config` / `sr_replace_config` | `:base:1.1` |
| `<delete-config>` | 7.4 | `sr_replace_config` (with NULL) | `:base:1.1` |
| `<commit>` | 8.3.4.1 | `sr_apply_changes` (candidate→running) | `:candidate` |
| `<discard-changes>` | 8.3.4.2 | `sr_discard_changes` | `:candidate` |
| `<validate>` | 7.5 | `sr_validate` | `:validate:1.1` |

**Non-goals for this phase:**
- NACM (RFC 6536) — separate phase.
- `<lock>` / `<unlock>` — already implemented.
- Notifications — separate phase.

## 2. sysrepo API mapping

sysrepo provides a **staged edit** model that maps directly to NETCONF's
candidate/commit flow:

```
NETCONF                    sysrepo
────────────────────────────────────────────────────────────────
<lock><target><candidate/>  sr_lock(candidate)
<edit-config>               sr_edit_batch(session, edit_tree, default_op)
  <default-operation>merge    → "merge" | "replace" | "none"
  <config>...</config>        → lyd_node tree parsed from <config> XML
<validate>                  sr_validate(session, NULL, 0)
<commit/>                   sr_apply_changes(session, 0)
<discard-changes/>          sr_discard_changes(session)
<unlock><target><candidate/> sr_unlock(candidate)
```

For `<edit-config>` on **running** directly (no candidate):
```
sr_edit_batch(session, edit_tree, default_op)
sr_apply_changes(session, 0)   // applies to running
```

For `<copy-config>`:
```
sr_copy_config(session, module_name, src_ds, 0)
```

For `<delete-config>` (only startup can be deleted):
```
sr_replace_config(session, module_name, NULL, 0)
```

## 3. sysrepoadapter interface extension

The `Session` interface gains edit methods:

```go
// in internal/sysrepoadapter/adapter.go

type Session interface {
    // ... existing methods ...
    Get(ctx context.Context, xpath string) (*DataNode, error)
    Lock(ds Datastore) error
    Unlock(ds Datastore) error

    // --- edit operations (phase 2) ---

    // EditBatch loads a parsed edit tree into the session's staging area.
    // defaultOp is "merge", "replace", or "none" (RFC 6241 §7.2).
    EditBatch(edit *DataNode, defaultOp string) error

    // ApplyChanges commits the staged edits to the current datastore.
    ApplyChanges(timeoutMs uint32) error

    // DiscardChanges discards all staged edits.
    DiscardChanges() error

    // Validate validates the current datastore + staged edits without
    // applying them.
    Validate(moduleName string, timeoutMs uint32) error

    // CopyConfig replaces the current session's datastore with the
    // contents of srcDatastore.
    CopyConfig(moduleName string, srcDatastore Datastore, timeoutMs uint32) error

    // ReplaceConfig replaces the current session's datastore with the
    // given config tree. If config is nil, the datastore is cleared
    // (used by <delete-config>).
    ReplaceConfig(moduleName string, config *DataNode, timeoutMs uint32) error

    Close() error
}
```

### Mock implementation

The mock gains an in-memory `pendingEdit *DataNode` field on `mockSession`:

- `EditBatch` stores the edit tree + default operation.
- `ApplyChanges` merges the edit into the mock's tree (simplified: replaces
  matching subtrees).
- `DiscardChanges` clears `pendingEdit`.
- `Validate` is a no-op (returns nil).
- `CopyConfig` copies the source datastore's tree.
- `ReplaceConfig` replaces or clears the tree.

## 4. Operation handlers

### 4.1 `<edit-config>`

```xml
<rpc message-id="N">
  <edit-config>
    <target><running/></target>       <!-- or <candidate/> -->
    <default-operation>merge</default-operation>
    <test-option>set</test-option>     <!-- optional -->
    <error-option>stop-on-error</error-option>  <!-- optional -->
    <config>                           <!-- or <url> -->
      <system xmlns="urn:ietf:params:xml:ns:yang:ietf-system">
        <hostname>new-hostname</hostname>
      </system>
    </config>
  </edit-config>
</rpc>
```

**Handler flow:**

1. Parse `<target>` → datastore name → `SwitchDS(ds)`.
2. Parse `<default-operation>` (default: `"merge"`).
3. Parse `<config>` inner XML → build a `DataNode` tree (using the
   `internal/data` encoder's inverse — an XML→DataNode decoder).
4. Call `sess.EditBatch(editTree, defaultOp)`.
5. Call `sess.ApplyChanges(0)`.
6. Return `<ok/>`.

**Error mapping:**

| sysrepo error | NETCONF error-tag |
|---|---|
| `SR_ERR_LOCKED` | `lock-denied` |
| `SR_ERR_VALIDATION_FAILED` | `operation-failed` + `error-app-tag: validation-failed` |
| `SR_ERR_DATA_EXISTS` (with `SR_EDIT_STRICT`) | `data-exists` |
| `SR_ERR_DATA_MISSING` | `data-missing` |
| `SR_ERR_UNAUTHORIZED` | `access-denied` |
| `SR_ERR_NOT_FOUND` | `data-missing` |

### 4.2 `<copy-config>`

```xml
<rpc message-id="N">
  <copy-config>
    <target><startup/></target>
    <source><running/></source>        <!-- or <url> or <config> -->
  </copy-config>
</rpc>
```

**Handler flow:**

1. Parse `<target>` → target datastore → `SwitchDS(target)`.
2. Parse `<source>`:
   - If `<running/>`/`<startup/>`/`<candidate/>` → call
     `sess.CopyConfig(NULL, srcDS, 0)`.
   - If `<config>` → parse to `DataNode`, call
     `sess.ReplaceConfig(NULL, configTree, 0)`.
   - If `<url>` → not supported in v1; return `operation-not-supported`.
3. Return `<ok/>`.

### 4.3 `<delete-config>`

```xml
<rpc message-id="N">
  <delete-config>
    <target><startup/></target>
  </delete-config>
</rpc>
```

**Handler flow:**

1. Parse `<target>` → must be `startup` (only startup can be deleted per
   RFC 6241 §7.4).
2. `SwitchDS(Startup)`.
3. `sess.ReplaceConfig(NULL, nil, 0)` — clears the datastore.
4. Return `<ok/>`.

### 4.4 `<commit>`

```xml
<rpc message-id="N">
  <commit/>
</rpc>
```

**Handler flow:**

1. The session must be on `candidate` (NETCONF commits candidate→running).
2. `sess.ApplyChanges(0)` — sysrepo applies staged candidate edits to
   running.
3. Return `<ok/>`.

**Confirmed commit** (RFC 6241 §8.3.4.1 + `:confirmed-commit:1.1`):
deferred to a later phase. The basic `<commit/>` is straightforward;
confirmed commit requires a timer + rollback which is more complex.

### 4.5 `<discard-changes>`

```xml
<rpc message-id="N">
  <discard-changes/>
</rpc>
```

**Handler flow:**

1. Session must be on `candidate`.
2. `sess.DiscardChanges()`.
3. Return `<ok/>`.

### 4.6 `<validate>`

```xml
<rpc message-id="N">
  <validate>
    <source><candidate/></source>
  </validate>
</rpc>
```

**Handler flow:**

1. Parse `<source>` → datastore → `SwitchDS(ds)`.
2. `sess.Validate("", 0)`.
3. Return `<ok/>`.

## 5. XML → DataNode decoder

`<edit-config>`'s `<config>` payload is NETCONF XML that must be converted
to a `DataNode` tree for sysrepo. The existing `internal/data` package has
an encoder (DataNode → XML); we need the inverse (XML → DataNode).

```go
// in internal/data/decoder.go

// DecodeData parses NETCONF XML into a DataNode tree.
func DecodeData(xmlBytes []byte) (*sysrepoadapter.DataNode, error)
```

The decoder walks the XML using `encoding/xml.Decoder`:
- Each `StartElement` becomes a `DataNode` (container/list/leaf).
- `CharData` becomes a leaf value.
- The element's namespace becomes `DataNode.NS`.
- List key predicates (`[name='eth0']`) are parsed from the element name
  or from child key leaves.

This decoder is used by `<edit-config>` and `<copy-config>` (when the
source is `<config>`).

## 6. Capability advertisement

Add to the `<hello>` capabilities:

| Capability | When to advertise |
|---|---|
| `urn:ietf:params:netconf:capability:candidate:1.0` | always (we support candidate) |
| `urn:ietf:params:netconf:capability:validate:1.1` | always (we support `<validate>`) |
| `urn:ietf:params:netconf:capability:rollback-on-error:1.0` | phase 2.5 (not yet) |
| `urn:ietf:params:netconf:capability:confirmed-commit:1.1` | phase 2.5 (not yet) |

The `server.buildCapabilities()` method adds these.

## 7. Session lifecycle changes

Currently each operation handler opens and closes its own short-lived
`sysrepoadapter.Session`. For edit operations this is wrong — the
staged edits must persist across multiple RPCs within the same NETCONF
session:

1. `<lock><candidate/>` — lock candidate
2. `<edit-config><target><candidate/>` — stage edits
3. `<validate>` — validate staged edits
4. `<commit/>` — apply staged edits to running
5. `<unlock><candidate/>` — unlock

**Change:** `ServeTransport` opens one `sysrepoadapter.Session` per
NETCONF session and passes it to all handlers via `Deps.Session`:

```go
// in internal/server/server.go ServeTransport:

sess, err := s.conn.OpenSession(ctx, state.User)
if err != nil { return err }
defer sess.Close()

deps := operations.Deps{
    Cache:    s.cache,
    Conn:     s.conn,
    Encoder:  s.encoder,
    Sessions: s.reg,
    Session:  sess,  // <-- per-NETCONF-session datastore session
}
handlers := operations.BuildHandlers(deps, sessionID, state.User)
```

The `Deps` struct gains a `Session sysrepoadapter.Session` field.
Operations that need the datastore use `deps.Session` instead of opening
a new one.

## 8. Registration

```go
// in internal/operations/operations.go Register():

d.Register("edit-config",     &editConfigHandler{deps: deps})
d.Register("copy-config",     &copyConfigHandler{deps: deps})
d.Register("delete-config",   &deleteConfigHandler{deps: deps})
d.Register("commit",          &commitHandler{deps: deps})
d.Register("discard-changes", &discardChangesHandler{deps: deps})
d.Register("validate",        &validateHandler{deps: deps})
```

## 9. Test plan

### Mock-based unit tests

- `TestEditConfig_Merge` — merge a leaf into the running datastore.
- `TestEditConfig_Replace` — replace a container.
- `TestEditConfig_Create` — create a new list entry.
- `TestEditConfig_Delete` — delete a list entry (operation=delete).
- `TestEditConfig_MissingTarget` — missing `<target>` → `missing-element`.
- `TestEditConfig_InvalidDefaultOp` — bad `default-operation` → `bad-element`.
- `TestCommit` — stage edits on candidate, commit to running.
- `TestDiscardChanges` — stage edits, discard, verify running unchanged.
- `TestCopyConfig_RunningToStartup` — copy running→startup.
- `TestCopyConfig_WithConfig` — copy from `<config>` source.
- `TestDeleteConfig_Startup` — delete startup config.
- `TestDeleteConfig_Running` — `delete-config` on running → `bad-element`.
- `TestValidate_OK` — validate a valid config.
- `TestValidate_Fails` — validate an invalid config → `operation-failed`.

### End-to-end SSH test

```
<hello> → <lock><candidate/> → <edit-config><candidate/> → <validate>
  → <commit/> → <unlock><candidate/> → <get-config><running/>
```

Verify the edited data appears in running after commit.

## 10. File changes

| File | Change |
|---|---|
| `internal/sysrepoadapter/adapter.go` | Add `EditBatch`, `ApplyChanges`, `DiscardChanges`, `Validate`, `CopyConfig`, `ReplaceConfig` to `Session` interface |
| `internal/sysrepoadapter/mock.go` | Implement the new methods on `mockSession` |
| `internal/sysrepoadapter/cgo_stub.go` | Implement the new methods via `sr_edit_batch`, `sr_apply_changes`, etc. (behind `sysrepo` tag) |
| `internal/data/decoder.go` | New: `DecodeData(xmlBytes) → *DataNode` (XML→DataNode decoder) |
| `internal/operations/editconfig.go` | New: `<edit-config>` handler |
| `internal/operations/copyconfig.go` | New: `<copy-config>` handler |
| `internal/operations/deleteconfig.go` | New: `<delete-config>` handler |
| `internal/operations/commit.go` | New: `<commit>` + `<discard-changes>` handlers |
| `internal/operations/validate.go` | New: `<validate>` handler |
| `internal/operations/operations.go` | Register new ops; add `Session` to `Deps` |
| `internal/server/server.go` | Open per-session `Session`; add `:candidate` + `:validate:1.1` capabilities |
| `internal/transport/dispatch.go` | No change (handlers are handlers) |
