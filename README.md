# chatops

## Usage

```bash
$ chatops --help
Usage: chatops <command>

Commands:
  version    Show build version.
```
### version

Prints build version, optionally with build time and source, in plain text or JSON:

```bash
$ chatops version
v0.1.0

$ chatops version --all --json
{"Version":"v0.1.0","BuildTime":"2026-07-15T22:04:35-0700","Source":"github"}
```

## Credential store (`cred`)

The `cred` package provides a generic way to access credentials from
pluggable backends. The top-level package defines the interface; each
backend lives in its own sub-package and exports the URL scheme it
serves plus an opener, which callers wire into a registry (no `init()`
side effects — supported backends are always visible at the wiring
site):

```go
type Store interface {
    // Get retrieves the credential identified by key. It returns an
    // error wrapping cred.ErrNotFound when the key does not exist.
    Get(ctx context.Context, key string) (string, error)

    // Close releases any resources held by the store.
    Close() error
}
```

A store is identified by a single URL — the scheme selects the
backend and the rest of the URL locates the store. Credentials for
accessing the store itself are **never** part of the URL; each backend
takes them from its standard environment variables (for example
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, `VAULT_TOKEN`), resolved
through the backend SDK's default configuration chain.

Available backends:

| Scheme       | Sub-package     | Store URL                        |
| ------------ | --------------- | -------------------------------- |
| `json-file`  | `cred/jsonfile` | `json-file:///path/to/file.json` |

How each backend lays out credentials in its store is backend-specific
and documented per backend below.

### Usage

Build a registry from the backends you want, open the store by URL,
then retrieve credentials by key:

```go
import (
    "context"
    "errors"

    "github.com/hangxie/chatops/cred"
    "github.com/hangxie/chatops/cred/jsonfile"
)

reg := cred.NewRegistry(
    cred.Backend{Scheme: jsonfile.Scheme, Opener: jsonfile.Opener},
)
store, err := reg.Open(context.Background(), "json-file:///etc/chatops/creds.json")
if err != nil {
    // handle error
}
defer store.Close()

secret, err := store.Get(context.Background(), "db-password")
if errors.Is(err, cred.ErrNotFound) {
    // credential does not exist
}
```

Backends also expose a typed `Open` function for direct use, e.g.
`jsonfile.Open(ctx, "/etc/chatops/creds.json")`.

### json-file backend

The store URL is the file path (relative paths work too:
`json-file://relative/path.json`). The file must contain a single JSON
object mapping credential keys to string values:

```json
{
  "db-password": "hunter2",
  "api-token": "abc123"
}
```

### Adding a new backend

1. Create a sub-package under `cred/` named after the backend (e.g.
   `cred/vault`).
2. Define a `Store` type implementing the `cred.Store` interface:
   - `Get` returns the credential for a key, wrapping
     `cred.ErrNotFound` (with `%w`) when the key does not exist so
     callers can detect it with `errors.Is`.
   - `Close` releases connections or other resources.
3. Provide an `Open` function taking `context.Context` plus
   backend-specific location parameters and returning `(*Store, error)`.
   Take credentials for the store from the backend's standard
   environment variables (prefer the official SDK's default
   configuration chain); never accept them as parameters.
4. Export the scheme and an opener so callers can wire the backend
   into a `cred.Registry` (backends never self-register via `init()`):

   ```go
   // Scheme is the URL scheme this backend serves in a cred.Registry.
   const Scheme = "my-backend"

   // Opener is the cred.OpenerFunc for this backend.
   func Opener(ctx context.Context, u *url.URL) (cred.Store, error) {
       return Open(ctx, u.Host+u.Path)
   }
   ```

5. Add a test file with table-driven tests covering `Open` failures,
   existing keys, missing keys, context cancellation, and opening
   through a `cred.Registry` with the exported scheme.
6. List the backend in the table above and document its store layout
   in a section like the json-file one.

## Chat backends (`chat`)

The `chat` package provides a generic way for the bot to talk to chat
backends (Slack, Discord, Mattermost, a naive telnet chat, ...). The
top-level package defines the interface; each backend lives in its own
sub-package and exports the URL scheme it serves plus an opener, which
callers wire into a registry (no `init()` side effects — supported
backends are always visible at the wiring site):

```go
type Conn interface {
    // Receive returns the next inbound message. It blocks until a
    // message arrives, ctx is done, the connection is lost, or Close
    // is called. After Close it reports an error wrapping ErrClosed.
    Receive(ctx context.Context) (Message, error)

    // Send posts msg.Text into the conversation identified by
    // msg.ConversationID. It returns an error wrapping
    // ErrUnknownConversation when the ID does not map to a
    // conversation the backend knows.
    Send(ctx context.Context, msg Message) error

    // Close terminates the connection, unblocking any pending Receive.
    Close() error
}
```

Messages are grouped into **conversations** — the topic or thread a
piece of work is about. Each backend computes a stable conversation ID
from its native addressing (e.g. a Slack backend derives it from
channel and thread; telnet has a single conversation) and translates
it back on send. Callers treat `Message.ConversationID` as an opaque
string scoped to one `Conn`: to reply, send with the `ConversationID`
of the message being answered.

A connection is identified by a single URL — the scheme selects the
backend and the rest of the URL locates the server. As with `cred`,
credentials for the backend itself are **never** part of the URL; each
backend takes them from its standard environment variables (for
example `SLACK_BOT_TOKEN`).

Available backends:

| Scheme   | Sub-package   | Connection URL       |
| -------- | ------------- | -------------------- |
| `telnet` | `chat/telnet` | `telnet://host:port` |

### Usage

Build a registry from the backends you want, open the connection by
URL, then receive and reply:

```go
import (
    "context"

    "github.com/hangxie/chatops/chat"
    "github.com/hangxie/chatops/chat/telnet"
)

reg := chat.NewRegistry(
    chat.Backend{Scheme: telnet.Scheme, Opener: telnet.Opener},
)
conn, err := reg.Open(context.Background(), "telnet://chat.example.com:6023")
if err != nil {
    // handle error
}
defer conn.Close()

for {
    msg, err := conn.Receive(context.Background())
    if err != nil {
        break // connection closed or lost
    }
    reply := chat.Message{ConversationID: msg.ConversationID, Text: "on it"}
    if err := conn.Send(context.Background(), reply); err != nil {
        // handle error
    }
}
```

Backends also expose a typed `Open` function for direct use, e.g.
`telnet.Open(ctx, "chat.example.com:6023")`.

### telnet backend

The connection URL is the server address; the port defaults to the
telnet port 23 (`telnet://chat.example.com` ≡
`telnet://chat.example.com:23`). The wire protocol is bare lines of
text: every newline-terminated line received is one inbound message
(blank lines are ignored), and `Send` writes the message text followed
by a newline. Telnet option negotiation (IAC sequences) is not
performed.

The connection carries a single conversation whose ID is the
`telnet.ConversationID` constant; the protocol has no notion of
identity, so `Message.Sender` is empty.

### Adding a new backend

1. Create a sub-package under `chat/` named after the backend (e.g.
   `chat/slack`).
2. Define a `Conn` type implementing the `chat.Conn` interface:
   - Compute `Message.ConversationID` on receive from the backend's
     native addressing (e.g. Slack channel + thread), and translate it
     back on send. Wrap `chat.ErrUnknownConversation` (with `%w`) when
     a sent ID does not map to a conversation.
   - After `Close`, `Receive` and `Send` report an error wrapping
     `chat.ErrClosed`; `Close` must also unblock a pending `Receive`.
3. Provide an `Open` function taking `context.Context` plus
   backend-specific location parameters and returning `(*Conn, error)`.
   Take credentials from the backend's standard environment variables;
   never accept them as parameters or URL elements.
4. Export the scheme and an opener so callers can wire the backend
   into a `chat.Registry` (backends never self-register via `init()`),
   and add it to the CLI's registry in `cmd/chat`:

   ```go
   // Scheme is the URL scheme this backend serves in a chat.Registry.
   const Scheme = "my-backend"

   // Opener is the chat.OpenerFunc for this backend.
   func Opener(ctx context.Context, u *url.URL) (chat.Conn, error) {
       return Open(ctx, u.Host)
   }
   ```

5. Add a test file with table-driven tests covering `Open` failures,
   receive/send round-trips, conversation ID mapping, context
   cancellation, `Close` semantics, and opening through a
   `chat.Registry` with the exported scheme.
6. List the backend in the table above and document its protocol and
   conversation ID scheme in a section like the telnet one.

## Tools (`tool`)

The `tool` package provides a generic way to invoke operational tools
(kubernetes, proxmox, harbor, a dummy ping tool, ...). The top-level
package defines the interface; each tool lives in its own sub-package
and exports the URL scheme it serves plus an opener, which callers
wire into a registry (no `init()` side effects — supported tools are
always visible at the wiring site):

```go
type Tool interface {
    // Invoke performs the operation described by call and returns its
    // outcome. It returns an error wrapping ErrUnknownAction when
    // call.Action is not one the tool supports.
    Invoke(ctx context.Context, call Call) (Result, error)

    // Close releases any resources held by the tool.
    Close() error
}
```

A call carries enough detail for the tool to act, without prescribing
how it maps to actual API calls or commands: an **action** (the verb,
e.g. `restart`), a **target** (what it applies to, e.g.
`deployment/web`; may be empty), and optional key-value
**parameters**. The result carries **text** — the human-readable
outcome, composed by the tool and ready to post to chat as-is — plus
optional machine-readable key-value **details**; callers never need
the details to render a reply.

A tool instance is identified by a single URL — the scheme selects the
implementation, host/port/path locate the endpoint it operates on, and
query parameters carry further instance configuration. Credential
*values* are **never** part of the URL; tools resolve them from the
`cred.Store` passed to `Open`. Each tool defines conventional key
names prefixed by its name (e.g. `k8s-ca`/`k8s-cert`/`k8s-key`,
`proxmox-ssh-user`/`proxmox-ssh-key`, `harbor-user`/`harbor-password`),
and the prefix can be overridden per instance with the `cred-prefix`
query parameter so multiple instances of the same tool can use
distinct credentials:

```
kubernetes://prod.example.com:6443?cred-prefix=k8s-prod
```

Available tools:

| Scheme | Sub-package | Tool URL  |
| ------ | ----------- | --------- |
| `ping` | `tool/ping` | `ping://` |

### Usage

Build a registry from the tools you want, open the tool by URL with a
credential store, then invoke actions:

```go
import (
    "context"
    "fmt"

    "github.com/hangxie/chatops/tool"
    "github.com/hangxie/chatops/tool/ping"
)

reg := tool.NewRegistry(
    tool.Backend{Scheme: ping.Scheme, Opener: ping.Opener},
)
tl, err := reg.Open(context.Background(), "ping://", nil) // creds not needed by ping
if err != nil {
    // handle error
}
defer tl.Close()

result, err := tl.Invoke(context.Background(), tool.Call{Action: "ping"})
if err != nil {
    // handle error
}
fmt.Println(result.Text) // "pong"
```

Tools also expose a typed `Open` function for direct use, e.g.
`ping.Open(ctx)`.

### ping tool

A dummy tool that answers `pong` to the `ping` action, useful as a
liveness check and as the reference implementation of the interface.
It has no endpoint and takes no credentials, so the URL is a bare
`ping://` (anything beyond the scheme — host, path, query, userinfo,
or non-empty fragment — is rejected; a bare trailing `#` parses
identically to the bare URL and is accepted). The only supported
action is `ping`; `Target` and `Parameters` are ignored, and any other
action reports an error wrapping `tool.ErrUnknownAction`.

### Adding a new tool

1. Create a sub-package under `tool/` named after the tool (e.g.
   `tool/kubernetes`).
2. Define a `Tool` type implementing the `tool.Tool` interface:
   - `Invoke` maps the call's action/target/parameters onto the tool's
     API, wrapping `tool.ErrUnknownAction` (with `%w`) when the action
     is not supported so callers can detect it with `errors.Is`.
   - Compose `Result.Text` as the complete human-readable answer; put
     supplementary machine-readable output in `Result.Details`.
   - `Close` releases connections or other resources.
3. Provide an `Open` function taking `context.Context` plus
   tool-specific parameters and returning `(*Tool, error)`. Resolve
   credentials from the `cred.Store` using the tool's conventional key
   names (document them), honoring the `cred-prefix` override; never
   accept credential values as parameters or URL elements.
4. Export the scheme and an opener so callers can wire the tool into a
   `tool.Registry` (tools never self-register via `init()`):

   ```go
   // Scheme is the URL scheme this tool serves in a tool.Registry.
   const Scheme = "my-tool"

   // Opener is the tool.OpenerFunc for this tool.
   func Opener(ctx context.Context, u *url.URL, creds cred.Store) (tool.Tool, error) {
       return Open(ctx, u.Host, creds)
   }
   ```

5. Add a test file with table-driven tests covering `Open` failures,
   supported and unknown actions, context cancellation, `Close`
   semantics, and opening through a `tool.Registry` with the exported
   scheme.
6. List the tool in the table above and document its actions and
   credential key names in a section like the ping one.
