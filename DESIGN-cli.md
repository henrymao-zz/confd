# Design: Interactive CLI Shell (`confd`)

> Proposes an interactive NETCONF client shell, invoked simply as `confd`
> (with no subcommand), similar to `netopeer2-cli`.

## 1. Overview

When the user runs `confd` with no subcommand (no `serve` or `schema-list`),
confd enters an interactive shell that connects to a NETCONF server (by
default localhost:830, or a host specified on the command line) and
provides a prompt for typing NETCONF operations.

```
$ confd
confd> connect 127.0.0.1:830
Connecting to 127.0.0.1:830...
Session 1 established (base:1.1)
confd> get-config --source running
<rpc-reply message-id="1">
  <data>
    <system xmlns="urn:ietf:params:xml:ns:yang:ietf-system">
      <hostname>router-1</hostname>
    </system>
  </data>
</rpc-reply>
confd> edit-config --target running
Entering edit mode (default-operation: merge)
confd> set /ietf-system:system/hostname new-host
confd> commit
<ok/>
confd> get-config --source running
<rpc-reply message-id="3">
  <data>
    <system xmlns="urn:ietf:params:xml:ns:yang:ietf-system">
      <hostname>new-host</hostname>
    </system>
  </data>
</rpc-reply>
confd> disconnect
confd> quit
```

## 2. Architecture

The CLI shell is a **NETCONF client** (not a server). It uses
`nemith.io/netconf`'s client `Session` (which we already depend on) to
connect to a NETCONF server over SSH. It does not need the server-side
code (`internal/transport.ServerLoop`, `internal/operations`, etc.) —
it only needs the nemith client transport + session.

```
confd (interactive shell)
  │
  ├── Readline loop (prompt → parse command → execute → display result)
  │
  ├── nemith.io/netconf client
  │   ├── transport/ssh.Dial → *Session
  │   ├── Session.Do / Session.Exec (send RPC, receive reply)
  │   └── rpc.GetConfig, rpc.EditConfig, rpc.Lock, etc. (typed ops)
  │
  └── No server-side code needed (no sysrepo, no cgo, no plugins)
```

## 3. Commands

### Session management
```
connect [<host>:<port>]     Connect to a NETCONF server (default: 127.0.0.1:830)
disconnect                  Close the current session
quit / exit                  Exit the shell
help                        List available commands
```

### NETCONF operations (RFC 6241)
```
get [--filter <xpath>]                              <get>
get-config --source <running|startup|candidate>     <get-config>
  [--filter <xpath>]
edit-config --target <running|candidate>            <edit-config>
  [--default-operation <merge|replace|none>]
  [--config <xml-string> | --file <path>]
copy-config --target <ds> --source <ds|config>      <copy-config>
delete-config --target <startup>                   <delete-config>
lock --target <ds>                                 <lock>
unlock --target <ds>                               <unlock>
commit                                             <commit>
discard-changes                                    <discard-changes>
validate --source <ds>                             <validate>
kill-session --session-id <N>                     <kill-session>
close-session                                      <close-session>
```

### Schema operations
```
get-schema --identifier <module> [--version <rev>]  <get-schema> (RFC 6022)
list-modules                                        List advertised capabilities
```

### Convenience
```
raw <xml>                    Send raw XML as an <rpc> and print the reply
show session                 Show session info (id, capabilities)
show capabilities            List server capabilities
```

## 4. Implementation

### 4.1 New package: `internal/cli`

```go
// internal/cli/shell.go

package cli

type Shell struct {
    session   *netconf.Session
    transport *ssh.Transport
    msgID     atomic.Uint64
    reader    *bufio.Reader
}

func New() *Shell { ... }

// Run starts the interactive prompt loop.
func (s *Shell) Run() error { ... }

// Command handlers:
func (s *Shell) cmdConnect(args []string) error { ... }
func (s *Shell) cmdGetConfig(args []string) error { ... }
func (s *Shell) cmdEditConfig(args []string) error { ... }
// ...
```

### 4.2 Entry point: `cmd/confd/main.go`

```go
func main() {
    if len(os.Args) < 2 {
        // No subcommand → interactive shell
        cli.New().Run()
        return
    }
    switch os.Args[1] {
    case "serve":
        runServe(os.Args[2:])
    case "schema-list":
        runSchemaList(os.Args[2:])
    default:
        // Unknown subcommand → try as CLI command (connect, etc.)
        // Or: treat the whole arg list as CLI args
        cli.New().Run(os.Args[1:]...)
    }
}
```

### 4.3 Using nemith's client API

The shell uses nemith's client `Session` to send RPCs:

```go
func (s *Shell) cmdGetConfig(args []string) error {
    source := flagValue(args, "--source", "running")
    ds := netconf datastore mapping
    
    // Using nemith's typed RPC:
    var reply rpc.GetConfigReply
    err := s.session.Exec(ctx, &rpc.GetConfig{
        Source: rpc.Running,
        Filter: ...,
    }, &reply)
    
    // Print the reply XML
    fmt.Println(reply.Data)
}
```

For operations not covered by nemith's typed RPCs (e.g., `<get-schema>`),
use `Session.Do` with a raw `RPC`:

```go
func (s *Shell) cmdGetSchema(args []string) error {
    ident := flagValue(args, "--identifier", "")
    rpc := netconf.NewRPC(&struct {
        XMLName xml.Name `xml:"get-schema"`
        Identifier string `xml:"identifier"`
        Version string `xml:"version,omitempty"`
    }{Identifier: ident})
    msg, err := s.session.Do(ctx, rpc)
    defer msg.Close()
    raw, _ := io.ReadAll(msg)
    fmt.Println(string(raw))
}
```

### 4.4 Readline

Use `bufio.Reader` on `os.Stdin` for the prompt loop. For line editing
(arrow keys, history), optionally use `github.com/chzyer/readline` or
`golang.org/x/term`. For the MVP, `bufio.Scanner` is sufficient.

### 4.5 Output formatting

NETCONF XML replies are pretty-printed with indentation. Use
`encoding/xml.MarshalIndent` for structured replies. Raw replies are
printed as-is.

## 5. Dependencies

| Dependency | Purpose | Already in go.mod? |
|---|---|---|
| `nemith.io/netconf` | Client Session, transport/ssh.Dial, typed RPCs | ✅ |
| `golang.org/x/crypto/ssh` | SSH client auth | ✅ (via nemith) |
| `github.com/chzyer/readline` | Line editing + history (optional) | ❌ (new) |

No cgo, no sysrepo, no plugins — the CLI is a pure Go NETCONF client.

## 6. File changes

| File | Change |
|---|---|
| `internal/cli/shell.go` | New: interactive shell with prompt loop + command dispatch |
| `internal/cli/commands.go` | New: command handlers (connect, get, get-config, edit-config, ...) |
| `cmd/confd/main.go` | Enter shell when no subcommand given |
| `go.mod` | Add `github.com/chzyer/readline` (optional, for line editing) |

## 7. Usage examples

```bash
# Start the server in one terminal
confd serve --bind=127.0.0.1:830 --password=confd --adapter=mock

# In another terminal, connect with the CLI
confd
confd> connect 127.0.0.1:830 --user confd --password confd
Session 1 established
confd> get-config --source running
<data/>
confd> edit-config --target running --config '<system xmlns="urn:ietf:params:xml:ns:yang:confd-test"><hostname>new-host</hostname></system>'
<ok/>
confd> get-config --source running
<data>
  <system xmlns="urn:ietf:params:xml:ns:yang:confd-test">
    <hostname>new-host</hostname>
  </system>
</data>
confd> quit
```

## 8. Phasing

| Phase | Scope |
|---|---|
| **C1** | Shell skeleton: prompt loop, connect/disconnect/quit, raw XML RPC |
| **C2** | Typed commands: get, get-config, edit-config, lock, unlock, commit, discard-changes |
| **C3** | Schema commands: get-schema, list-modules, show capabilities |
| **C4** | Copy-config, delete-config, validate, kill-session, close-session |
| **C5** | Pretty-printing, history, tab completion (optional readline) |
