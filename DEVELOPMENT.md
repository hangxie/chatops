# Developing chatops

This guide describes the internal packages and how to add credential stores, chat backends, tools, and planners. For installation, configuration, and command usage, see the [user guide](README.md).

## Engine (`engine`)

The `engine` package joins the component interfaces into the server loop. It receives messages from one `chat.Conn`, passes each message and its connection metadata to a `planner.Planner`, calls the returned tool steps in order through an `mcphost.Host`, and sends every non-empty tool result back to the originating conversation. A planner asks for clarification or confirmation by returning a `reply` step, so multi-message interaction state stays in the planner instead of the engine.

```go
e, err := engine.New(engine.Config{
    ConnectionID: "operations",
    Chat:         conn,
    Planner:      p,
    Tools:        host,
})
if err != nil {
    // handle error
}
if err := e.Run(ctx); err != nil {
    // handle processing or cleanup error
}
```

`Run` preserves message order within each conversation while processing independent conversations concurrently through a fixed-size worker pool. The pool and its bounded backlog prevent messages from creating unbounded worker goroutines; `Config.MaxConcurrency` controls the worker count and defaults to `engine.DefaultMaxConcurrency`. A failure while handling one message — a bad plan, an unknown tool, a server that cannot be reached — posts a generic notice to the requester, is logged in full, and leaves the engine running. Context cancellation and a connection deliberately closed through `chat.Conn.Close` are graceful outcomes. A remote disconnect such as telnet EOF is a connection failure and is returned, allowing the caller or service supervisor to decide whether to reconnect or restart.

A tool that *ran* and reported its own failure is different: MCP carries that as `IsError` on the result rather than as a protocol error, so the engine relays the tool's own message to the requester instead of the generic notice. Panics are contained where they happen — a tool's inside its server (see `mcpserve`), and the planner's and host tools' at the message boundary in the engine — so no single misbehaving component can stop a long-running bot.

The engine owns and closes the chat connection, the planner, and the tool host after `New` succeeds. Unlike the per-step tool instances it used to open, MCP sessions are long-lived: the host connects every server once at startup and keeps the sessions for the engine's lifetime.

Tools that act on the requester's conversation do not take it as an argument. The engine puts it on the context with `chat.WithConversation`, and the reply tool reads it back with `chat.ConversationFrom`, so the conversation is host state the model never selects and a bad plan cannot redirect a reply.

## Credential store (`cred`)

The `cred` package provides a generic way to access credentials from pluggable backends. The top-level package defines the interface; each backend lives in its own sub-package and exports the URL scheme it serves plus an opener, which callers wire into a registry (no `init()` side effects — supported backends are always visible at the wiring site):

```go
type Store interface {
    // Get retrieves the credential identified by the predefined key. It returns an
    // error wrapping cred.ErrNotFound when the key does not exist.
    Get(ctx context.Context, key cred.Key) (string, error)

    // Close releases any resources held by the store.
    Close() error
}
```

A store is identified by a single URL — the scheme selects the backend and the rest of the URL locates the store. Credentials for accessing the store itself are **never** part of the URL; each backend takes them from its standard environment variables (for example `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, `VAULT_TOKEN`), resolved through the backend SDK's default configuration chain.

Available backends:

| Scheme      | Sub-package     | Store URL                        |
| ----------- | --------------- | -------------------------------- |
| `json-file` | `cred/jsonfile` | `json-file:///path/to/file.json` |

`cred.Key` is a closed set of application credential identifiers: `cred.SlackBotToken`, `cred.SlackAppToken`, and `cred.PlannerAPIKey`. Store backends map those identifiers to their native layout, preventing callers from constructing arbitrary names or configurable prefixes.

### Usage

Build a registry from the backends you want, open the store by URL, then retrieve credentials by key:

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

secret, err := store.Get(context.Background(), cred.PlannerAPIKey)
if errors.Is(err, cred.ErrNotFound) {
    // credential does not exist
}
```

Backends also expose a typed `Open` function for direct use, e.g. `jsonfile.Open(ctx, "/etc/chatops/creds.json")`.

### json-file backend

The store URL is the file path (relative paths work too: `json-file://relative/path.json`, and a leading `~` expands to the home directory: `json-file://~/creds.json`). The file uses a strict schema whose sections and credentials are optional:

```json
{
  "slack": {
    "bot-token": "xoxb-...",
    "app-token": "xapp-..."
  },
  "planner": {
    "api-key": "sk-..."
  }
}
```

Unknown sections, unknown fields, nulls, and non-string values are rejected by `Open`. Missing credentials remain absent and cause `Get` to wrap `cred.ErrNotFound`; present empty strings are returned so the consuming component can report that its required credential is empty.

### Adding a new backend

1. Create a sub-package under `cred/` named after the backend (e.g. `cred/vault`).
2. Define a `Store` type implementing the `cred.Store` interface:
   - `Get` returns the credential for a key, wrapping `cred.ErrNotFound` (with `%w`) when the key does not exist so callers can detect it with `errors.Is`.
   - `Close` releases connections or other resources.
3. Provide an `Open` function taking `context.Context` plus backend-specific location parameters and returning `(*Store, error)`. Take credentials for the store from the backend's standard environment variables (prefer the official SDK's default configuration chain); never accept them as parameters.
4. Export the scheme and an opener so callers can wire the backend into a `cred.Registry` (backends never self-register via `init()`):

   ```go
   // Scheme is the URL scheme this backend serves in a cred.Registry.
   const Scheme = "my-backend"

   // Opener is the cred.OpenerFunc for this backend.
   func Opener(ctx context.Context, u *url.URL) (cred.Store, error) {
       return Open(ctx, u.Host+u.Path)
   }
   ```

5. Add a test file with table-driven tests covering `Open` failures, existing keys, missing keys, context cancellation, and opening through a `cred.Registry` with the exported scheme.
6. List the backend in the table above and document its store layout in a section like the json-file one.

Adding a new application credential requires adding its identifier and section/field path to the schema table in `cred/cred.go`. `Key.String` and schema-aware store backends derive their mappings from that table.

## Chat backends (`chat`)

The `chat` package provides a generic way for the bot to talk to chat backends (Slack, Discord, Mattermost, a naive telnet chat, ...). The top-level package defines the interface; each backend lives in its own sub-package and exports the URL scheme it serves plus an opener, which callers wire into a registry (no `init()` side effects — supported backends are always visible at the wiring site):

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

Messages are grouped into **conversations** — the topic or thread a piece of work is about. Each backend computes a stable conversation ID from its native addressing (e.g. a Slack backend derives it from channel and thread; telnet has a single conversation) and translates it back on send. Callers treat `Message.ConversationID` as an opaque string scoped to one `Conn`: to reply, send with the `ConversationID` of the message being answered.

A connection is identified by a single URL — the scheme selects the backend and the rest of the URL locates the server. Credential values are **never** part of the URL; backends resolve predefined `cred.Key` identifiers from the `cred.Store` passed to `Registry.Open`. A backend that needs no credentials ignores the store.

Available backends:

| Scheme   | Sub-package   | Connection URL       |
| -------- | ------------- | -------------------- |
| `slack`  | `chat/slack`  | `slack://`            |
| `telnet` | `chat/telnet` | `telnet://host:port` |

### Usage

Build a registry from the backends you want, open the connection by URL, then receive and reply:

```go
import (
    "context"

    "github.com/hangxie/chatops/chat"
    "github.com/hangxie/chatops/chat/telnet"
)

reg := chat.NewRegistry(
    chat.Backend{Scheme: telnet.Scheme, Opener: telnet.Opener},
)
conn, err := reg.Open(context.Background(), "telnet://chat.example.com:6023", credentials)
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

Backends also expose a typed `Open` function for direct use, e.g. `telnet.Open(ctx, "chat.example.com:6023")`.

### Slack backend

The Slack backend uses the Events API and interactive payloads over Socket Mode for inbound messages, `chat.postMessage` for outbound replies, and `chat.update` to remove consumed controls. Its `slack://` URL takes no host, path, or query configuration. It resolves the bot OAuth token from `cred.SlackBotToken` (`slack.bot-token`) and the app-level token with `connections:write` from `cred.SlackAppToken` (`slack.app-token`); see the user guide for the required app event subscriptions and bot scopes. Startup calls `auth.test` to validate the bot token and obtain its user ID before opening Socket Mode.

Every accepted Socket Mode envelope is acknowledged before its event is processed. Human message and `app_mention` events become `chat.Message` values only when their text starts with the exact `<@USERID>` obtained for the authenticated bot. The backend strips that stable bot identity before planning, so changing the bot's display name requires no ChatOps configuration. Mentions of other users, bot mentions later in the text, unmentioned messages, bot messages, message subtypes, events without a sender, and empty commands are ignored. The conversation ID combines the Slack channel ID with the root message timestamp. A root message and all replies in its thread therefore share one engine conversation, and `Send` posts back into that thread. Routing entries refresh on receive and send, expire after 24 hours of inactivity, and are limited to 4,096 entries. An indexed min-heap makes refresh, expiry, and capacity eviction O(log n); reaching capacity evicts the earliest-expiring route.

`chat.Message.Choices` carries optional label/value responses. The reply tool maps the `choices` argument of a call into that backend-neutral field. Slack renders them as Block Kit buttons and treats a click as an ordinary inbound message containing the selected value; telnet ignores the metadata and sends the message's fallback text. Slack accepts only values registered for a prompt message posted by this process. Prompt routes expire after ten minutes, are capped at 4,096 entries, and are atomically removed on selection, which rejects expired, foreign, unregistered, and duplicate clicks without unbounded state. A valid click clears the message's buttons before delivery to the planner.

### telnet backend

The connection URL is the server address; the port defaults to the telnet port 23 (`telnet://chat.example.com` ≡ `telnet://chat.example.com:23`). The wire protocol is bare lines of text: every newline-terminated line received is one inbound message (blank lines are ignored), and `Send` writes the message text followed by a newline. Telnet option negotiation (IAC sequences) is not performed.

The connection carries a single conversation whose ID is the `telnet.ConversationID` constant; the protocol has no notion of identity, so `Message.Sender` is empty.

### Adding a new backend

1. Create a sub-package under `chat/` named after the backend (e.g. `chat/slack`).
2. Define a `Conn` type implementing the `chat.Conn` interface:
   - Compute `Message.ConversationID` on receive from the backend's native addressing (e.g. Slack channel + thread), and translate it back on send. Wrap `chat.ErrUnknownConversation` (with `%w`) when a sent ID does not map to a conversation.
   - After `Close`, `Receive` and `Send` report an error wrapping `chat.ErrClosed`; `Close` must also unblock a pending `Receive`.
3. Provide an `Open` function taking `context.Context`, a `cred.Store`, and any backend-specific location parameters, and returning `(*Conn, error)`. Resolve credential values using predefined `cred.Key` identifiers; never accept values as URL elements.
4. Export the scheme and an opener so callers can wire the backend into a `chat.Registry` (backends never self-register via `init()`), and add it to the CLI's shared registry wiring in `internal/registry` (used by both `cmd/server` and `cmd/chats`):

   ```go
   // Scheme is the URL scheme this backend serves in a chat.Registry.
   const Scheme = "my-backend"

   // Opener is the chat.OpenerFunc for this backend.
   func Opener(ctx context.Context, u *url.URL, creds cred.Store) (chat.Conn, error) {
       return Open(ctx, creds, u.Host)
   }
   ```

5. Add a test file with table-driven tests covering `Open` failures, receive/send round-trips, conversation ID mapping, context cancellation, `Close` semantics, and opening through a `chat.Registry` with the exported scheme.
6. List the backend in the table above and document its protocol and conversation ID scheme in a section like the telnet one.

## Tools (`mcphost`, `mcpserve`, `tool`)

Operational tools are **Model Context Protocol** tools. chatops does not define a tool interface of its own: a tool is an `mcp.Tool` with a JSON Schema, a call is `tools/call`, and a result is an `mcp.CallToolResult`. Three packages divide the work:

| Package    | Role                                                                         |
| ---------- | ---------------------------------------------------------------------------- |
| `tool/*`   | the tool implementations, each registering itself on an `*mcp.Server`        |
| `mcpserve` | assembles the built-in tools into MCP servers, one per group                 |
| `mcphost`  | the client half: connects servers and presents their tools as one catalog    |

### Built-in servers are real servers

Built-in groups run in this process, each on its own goroutine, reached over the SDK's in-memory transport. That transport is not a shortcut: it is `net.Pipe` driving the same newline-delimited JSON codec the stdio transport uses, so every call is a real JSON round trip with a real `initialize` handshake, real schema validation, and real cancellation. There is deliberately **no** direct-call path that bypasses it.

This is what makes a group splittable out later without touching its code. Moving one out of process is a change of `ServerSpec` at the wiring site — and once `chatops mcp serve` is used (see the user guide), a change of configuration and nothing more. The rule that keeps it true: **nothing crosses the boundary except JSON**. A group may not close over host state, and all of its operator configuration must be expressible as server options.

### Groups

A group is the unit of deployment, not just of organization. Each becomes its own `*mcp.Server` and its own session, so one group can move out of process while the rest stay in.

| Group    | Sub-package   | Tools                        | Options                      |
| -------- | ------------- | ---------------------------- | ---------------------------- |
| `ping`   | `tool/ping`   | `ping`                       | none                         |
| `status` | `tool/status` | `status-check`, `status-list` | none                         |
| `k8s`    | `tool/k8s`    | `k8s-list`, `k8s-get`        | `context`, `kubeconfig`      |

Options are non-secret instance configuration written as a query on the group selector (`k8s?context=prod`). Credential *values* are never options; groups resolve predefined `cred.Key` identifiers from the `cred.Store` passed to their `RegisterFunc`.

A `RegisterFunc` validates its options — a misspelled one is an operator mistake worth catching at startup — but must not do I/O that can fail. Every group is served by default, so a `k8s` group that loaded its kubeconfig at registration would stop a bot that never meant to talk to Kubernetes from starting at all. Resolve such a dependency on first use instead (see `tool/k8s/lazy.go`), which turns it into an error on that group's tools alone.

### The host catalog

`mcphost.Host` connects every configured server, caches each one's tool list, and presents the union as one catalog.

```go
host, err := mcphost.New(ctx, mcphost.Config{
    Servers:   servers,                      // from internal/builtin
    HostTools: []mcphost.HostTool{replyTool}, // tools this process serves itself
    Allow:     []string{"k8s-*"},            // optional name filter
})
tools := host.Tools(ctx)
result, err := host.CallTool(ctx, "k8s-get", map[string]any{"kind": "pod", "name": "web-0"})
```

Points worth knowing:

- **The catalog is live.** A server that reports `tools/list_changed` refreshes the session's cache and bumps `Host.Generation()`, so a planner caching function definitions against that value picks the change up without being reopened.
- **Listing cannot fail.** A server that cannot be listed contributes nothing and says so in the logs; failing the requester's message because one of several servers is unwell would be the wrong response, so `Tools` returns what is available and may simply return less.
- **Names are qualified and sanitized.** A server's `Alias` prefixes its tools (`github-search_repos`); an empty alias leaves them under their own names, which is what the built-in groups use because their names are already distinct. Names are coerced into the character set LLM tool-use APIs accept and capped at 64 characters, with a hash suffix if one has to be shortened. A name claimed twice is offered once, and the shadowed tool is reported.
- **Timeouts are hard bounds, and separate ones.** `Client.Connect` and a pending `tools/list` do not honour context cancellation on a transport whose peer has gone quiet, so the host bounds both itself (`ConnectTimeout`, `ListTimeout`). Without them one unresponsive server would hang startup, or wedge a refresh that has no caller to cancel it. The two budgets are independent: a slow handshake must not eat the time allowed for the listing that follows it, so bringing a server up can take up to the sum.
- **A failed refresh keeps the tools already known.** Nothing retries a listing — the only thing that starts one is the server reporting another change — so clearing the cache on a single timeout would drop that server from the catalog for as long as it stayed quiet. The initial listing is the exception: there is no previous list to keep, so the session is refused outright.
- **Startup is strict, running is lenient.** A server that cannot be connected or listed fails `New`, which suits built-in groups but is the opposite of the running rule. See [Deferred decisions](#deferred-decisions).

### What the SDK does that the code works around

Five behaviours of `modelcontextprotocol/go-sdk` shape this package and are not obvious from its documentation. Each was found by measurement, and each is the reason a piece of code looks the way it does — so changing that code without knowing them will reintroduce the bug it fixes.

1. **Handler panics are not recovered.** Each request runs on its own goroutine, and an unrecovered panic takes the process down — which for an in-process group is the bot. `mcpserve.RecoverMiddleware` contains them. It is registered *after* the group, because the SDK wraps each middleware around what is already there: last added is outermost, so a guard added first sits inside anything the group installed.

2. **`Client.Connect` does not honour its context.** It blocks writing the initialize request until the peer reads it, and cancelling does not interrupt that write — measured at three leaked goroutines per attempt against a peer that never appears. Closing the connection does interrupt it, which is why `mcphost` connects through a transport that retains the connection so it can be closed on timeout.

3. **`Client.Connect`'s context bounds only the handshake.** A session outlives the context that created it, which is what makes a connect timeout safe to impose at all. It also means the handshake budget must not be reused for the listing that follows: deriving from a context that already carries a deadline keeps the earlier one, so `ListTimeout` would be silently capped by whatever the handshake left.

4. **A pending `tools/list` can outlive its context too.** A server that accepts the request and goes quiet would wedge a refresh that has no caller to cancel it, so the session bounds every listing itself.

5. **`http.Client`'s own timeout looks like a cancellation.** Its error satisfies `errors.Is(err, context.DeadlineExceeded)` and arrives wrapped in a `*url.Error` carrying the address it was calling. Anything deciding "the caller went away" from the error would relay that address; `mcpserve.Curate` asks the caller's context instead.

One more, on the other side of the boundary: the in-memory transport is `net.Pipe` driving the same newline-delimited JSON codec as stdio, so it is a full protocol round trip rather than a shortcut — which is what makes an in-process group and a split-out one behave identically.

### Deferred decisions

Two host policies suit the built-in groups and will not suit servers a network away. Both are settled deliberately rather than by default, and both should be revisited together with the design for connecting external servers, since that is what makes them matter:

- **Startup is strict.** `mcphost.New` fails if any server cannot be connected or listed. For a built-in group that is a bug or a misconfiguration worth stopping for. For one external server among several it contradicts the rule applied everywhere else — that a server which goes quiet costs only its own tools — so the lenient rule belongs with reconnection and backoff rather than on its own. Whether it should be the rule or an option is part of that decision: an operator who lists a server explicitly may well want startup to fail without it.
- **Servers connect serially.** The worst case is `ConnectTimeout + ListTimeout` per server, which is immaterial in process and is not once a handshake crosses a network. Connecting concurrently must keep the order of the spec list, because that order is what decides which server keeps a name two of them claim.

### Host tools

A host tool is served by this process rather than by an MCP server, because it acts on host state. `reply` is the only one: it is bound to the live `chat.Conn`, which no server could hold. It appears in the same catalog and is called the same way — exactly as an MCP host adds its own local tools to what it offers the model.

Host tools are **not** subject to `Allow`. They are what the host itself can do rather than part of the operational surface an operator curates, and a planner has no way to express a reply without the reply tool: model prose becomes a reply step unconditionally, so filtering `reply` out would leave a bot that cannot answer at all.

### Adding a new tool

Adding a tool is now a typed input struct and one `mcp.AddTool` call. There is no scheme, opener, or descriptor to write: the SDK infers the input schema from the struct and validates arguments against it before the handler runs.

1. Pick a group — an existing sub-package under `tool/`, or a new one if the tool is a new deployment unit.
2. Define the input as a struct, with a `json` tag per argument and a `jsonschema` tag describing it to the model. Fields without `omitempty` are required.

    ```go
    // GetArgs is the input schema of the k8s-get tool.
    type GetArgs struct {
        Kind      string `json:"kind" jsonschema:"Resource type, e.g. pod or statefulset."`
        Namespace string `json:"namespace,omitempty" jsonschema:"Namespace; defaults to the context's."`
    }
    ```

3. Register the tool in the group's `RegisterFunc`, composing the complete human-readable answer as text content. Return an error for a bad argument — the SDK turns it into a tool result with `IsError` set, which is what lets the requester see what they got wrong.

    **A tool result is relayed into chat, so treat it as output, not as a log line.** An error about the call itself is exactly what the requester needs. Anything the tool learns from the system behind it is not: a kubeconfig path, an API server address, the identity an authorization check rejected. Log those through `opts.Logger` and return a short curated message instead — `tool/k8s` does this in `curate`, marking its own argument errors with `invalidCall` and replacing everything reaching it from client-go. Panics are handled for you; they fail as protocol errors and never reach chat.

    ```go
    // GroupName is the built-in group this package registers into.
    const GroupName = "k8s"

    func Register(s *mcp.Server, opts mcpserve.Options) error {
        if err := mcpserve.CheckOptions(GroupName, opts.Query, optionContext); err != nil {
            return err
        }
        mcp.AddTool(s, &mcp.Tool{
            Name:        "k8s-get",
            Description: "One-line, model-facing description of the tool.",
        }, func(ctx context.Context, _ *mcp.CallToolRequest, args GetArgs) (*mcp.CallToolResult, any, error) {
            return getResources(ctx, client, args)
        })
        return nil
    }
    ```

4. Wire the group into `internal/registry.Builtin()` if it is new:

    ```go
    mcpserve.Group{Name: toolk8s.GroupName, Register: toolk8s.Register}
    ```

5. Add a test file with table-driven tests covering valid and invalid arguments, option validation, context cancellation, and at least one call made **through a session** (`testutils.MCPSession`) so the declared schema is exercised the way a real caller would exercise it.
6. List the tool in the group table above and document its arguments and credential identifiers.

Keep in mind that the schema is the contract the model sees. Typed arguments are worth using: `all-namespaces` is a real boolean, so the server rejects `"maybe"` before the tool ever runs.

## Planners (`planner`)

The `planner` package provides a generic way to turn free-form chat messages into executable plans, backed by pluggable planner backends — the OpenAI Chat Completions backend (which also drives compatible services such as Gemini and Ollama), Anthropic (planned), or the dummy ping planner. The top-level package defines the interface; each backend lives in its own sub-package and exports the URL scheme it serves plus an opener, which callers wire into a registry (no `init()` side effects — supported backends are always visible at the wiring site):

```go
type Planner interface {
    // Plan decides what to do about one inbound message and returns
    // the steps to execute. Asking the requester a clarifying question
    // is expressed as a step invoking the reply tool, not as an error.
    Plan(ctx context.Context, req Request) (Plan, error)

    // Close releases any resources held by the planner.
    Close() error
}
```

A request carries the message **text**, the **conversation ID** and **sender** (both as computed by the chat backend, see `chat.Message`), and a caller-assigned **connection ID**; planners use the connection and conversation IDs together to keep per-conversation context across requests. The connection ID exists because conversation IDs are only unique within one `chat.Conn` (every telnet connection reports the same one, for example): a caller serving several connections from one planner must give each connection a distinct opaque ID, while a caller with a single connection may leave it empty. The returned plan is a sequence of **steps**, each naming a tool from the catalog the planner was opened with plus the arguments to call it with. Replying to the requester is itself a step — one invoking the `reply` tool — so a clarifying question and an operational action have the same shape, mirroring how LLM tool-use APIs treat text output and tool calls as peers in one turn.

Steps name tools only, so a plan is **not self-contained**: the caller executes it in the context of the request that produced it. In particular the `reply` tool posts into the conversation the caller puts on the context, which is what keeps replies on the right connection even when conversation IDs collide across connections. A step never names a conversation of its own.

A planner is identified by a single URL — the scheme selects the backend, host/port/path locate the endpoint it talks to (empty for providers with a well-known API endpoint), and query parameters carry further configuration such as the model (e.g. `openai-chat-completions://api.openai.com/v1?model=gpt-5`, `anthropic://?model=claude-fable-5`). Credential *values* are **never** part of the URL. Because the server runs one planner, an authenticated backend resolves the single `cred.PlannerAPIKey`; caller-selected credential prefixes are not supported.

`Open` also receives the caller's enabled tool catalog as a `planner.ToolSource`, so an LLM-backed backend can offer those tools to the model as callable functions and emit plan steps naming them. A backend that plans a fixed set of steps (such as `ping`) ignores it. Backend `OpenerFunc` implementations take the trailing `tools ToolSource` parameter (never nil, possibly empty).

The source is **live**, not a snapshot: `Tools` returns the current catalog and `Generation` changes whenever it does, so a backend caches what it derives from the catalog against that value and rebuilds only when it moves. This is what lets a server's `tools/list_changed` reach the model without reopening the planner. Listing cannot fail — see the host catalog notes above.

Available backends:

| Scheme                     | Sub-package                       | Planner URL                                         |
| -------------------------- | --------------------------------- | --------------------------------------------------- |
| `openai-chat-completions`  | `planner/openaichatcompletions`   | `openai-chat-completions://host[:port][/path]?model=NAME` |
| `ping`                     | `planner/ping`                    | `ping://`                                           |

### Usage

Build a registry from the backends you want, open the planner by URL with a credential store and the enabled tool set, then plan inbound messages and execute the steps:

```go
import (
    "context"

    "github.com/hangxie/chatops/planner"
    planneropenaichat "github.com/hangxie/chatops/planner/openaichatcompletions"
    "github.com/hangxie/chatops/planner/ping"
)

reg := planner.NewRegistry(
    planner.Backend{Scheme: planneropenaichat.Scheme, Opener: planneropenaichat.Opener},
    planner.Backend{Scheme: ping.Scheme, Opener: ping.Opener},
)
// tools is the enabled planner.ToolSource (an *mcphost.Host, typically);
// nil is treated as the empty catalog. creds and tools are passed
// through to the backend's opener.
p, err := reg.Open(context.Background(), "ping://", nil, tools)
if err != nil {
    // handle error
}
defer p.Close()

plan, err := p.Plan(context.Background(), planner.Request{
    Text:           msg.Text,
    ConversationID: msg.ConversationID,
    Sender:         msg.Sender,
})
if err != nil {
    // handle error
}
ctx = chat.WithConversation(ctx, msg.ConversationID)
for _, step := range plan.Steps {
    // call step.Tool ("ping", "reply", ...) with step.Arguments through
    // the host, and post any non-empty rendered result back into the
    // conversation
    result, err := host.CallTool(ctx, step.Tool, step.Arguments)
    _ = mcphost.Render(result)
}
```

Backends also expose a typed `Open` function for direct use, e.g. `ping.Open(ctx)`.

### ping planner

A dummy planner that recognizes only the ping intent, useful as a wiring check and as the reference implementation of the interface. It talks to no LLM endpoint and takes no credentials, so the URL is a bare `ping://` (anything beyond the scheme is rejected, same rules as the ping tool).

- A message that is exactly `ping` (ignoring case and surrounding whitespace) plans an invocation of the ping tool.
- A message that merely contains `ping` as a standalone word (so `can you ping the box?` counts, `pinging` or `shipping` do not) plans a reply asking `do you want me to ping? (yes/no)` with Yes and No choices and remembers the pending question for that conversation.
- The next message in that conversation answers it: `yes`/`y` plans the ping, `no`/`n` plans an acknowledging reply, and anything else drops the pending confirmation without pinging and is handled as a fresh message. Each conversation — scoped by connection and conversation ID, so the same conversation ID on another chat connection cannot answer the question — holds at most one pending confirmation (a repeated ask just renews it), and conversations do not affect each other.
- Pending confirmations are bounded state: an unanswered confirmation expires after ten minutes, and at most 1024 conversations' confirmations are remembered at once (asking past the cap evicts the oldest).
- Everything unrecognized plans a reply saying `sorry, I don't understand`.

### openai-chat-completions planner

A planner backed by any service that speaks the OpenAI Chat Completions API, so the same backend drives OpenAI, Google Gemini's OpenAI-compatible endpoint, a local Ollama, vLLM, LocalAI, and similar servers. The endpoint is configured through the URL: the host is required (the planner is not tied to a fixed provider) and locates the endpoint, whose path defaults to `/v1`, with `insecure=true` selecting plain HTTP. The `model` query parameter is required (there is no universal default across services). By default the backend requires `cred.PlannerAPIKey` (`planner.api-key`) and sends it as a bearer token. `keyless=true` explicitly selects an unauthenticated endpoint; a missing or empty key is otherwise a startup error.

- The host is required, so a hostless or mistyped URL (e.g. the typo `openai-chat-completions:///host/v1` with three slashes, which parses to an empty host) is rejected rather than silently defaulting to some provider.
- On each message the planner makes one Chat Completions request, offering every tool in the catalog as a function named for the tool and carrying the tool's own input schema. The catalog already includes `reply`, so the planner needs no special case for it.
- MCP places no restriction on a tool's input schema beyond it being JSON Schema, but the completion endpoints accept only a subset and disagree about which — Gemini's OpenAI-compatible endpoint rejects `additionalProperties`, and several reject `$ref`, `oneOf`, `allOf`, and `not`. Each schema is therefore **downgraded** before it is offered (`schema.go`): local `$ref`s are inlined, a single-branch `oneOf`/`anyOf` (how a nullable value is usually spelled) is collapsed, a single-valued `const` becomes a one-entry `enum`, and unsupported keywords are dropped. Downgrading is lossy on purpose — a dropped keyword only widens what the model may send, and the server validates the call anyway.
- Downgrading is per tool, not per request. A tool whose name an endpoint cannot accept as a function name, or whose schema cannot be downgraded at all (an unresolvable `$ref`), is **skipped with a warning**; one bad tool from an external server must not make every request fail.
- The model's response maps to plan steps: assistant prose and each `reply` call become `reply` steps, and each other tool call becomes a step invoking that tool. A call naming a function that was not offered is an error; argument *values* are not checked here, because the tool's schema is the server's to enforce and it reports a violation as a tool error the requester can see.
- The exchange is single-shot — tool results are not fed back to the model — and the planner keeps no per-conversation history yet.

A typical exchange:

```text
user> can you ping the box?
bot>  do you want me to ping? (yes/no)
user> yes
bot>  pong
```

### Adding a new backend

1. Create a sub-package under `planner/` named after the backend (e.g. `planner/openaichatcompletions`, `planner/anthropic`).
2. Define a `Planner` type implementing the `planner.Planner` interface:
   - `Plan` turns one inbound message into steps; express replies and clarifying questions as steps invoking the `reply` tool with the text in `Arguments["text"]` (the executor supplies the target conversation on the context). Keep any per-conversation context keyed by the `(ConnectionID, ConversationID)` pair — never by `ConversationID` alone, which collides across chat connections — and make the planner safe for concurrent use.
   - `Close` releases connections or other resources.
3. Provide an `Open` function taking `context.Context` plus backend-specific parameters and returning `(*Planner, error)`. Resolve authentication from `cred.PlannerAPIKey` when needed; never accept credential values as parameters or URL elements.
4. Export the scheme and an opener so callers can wire the backend into a `planner.Registry` (backends never self-register via `init()`):

   ```go
   // Scheme is the URL scheme this backend serves in a planner.Registry.
   const Scheme = "my-llm"

   // Opener is the planner.OpenerFunc for this backend.
   func Opener(ctx context.Context, u *url.URL, creds cred.Store) (planner.Planner, error) {
       return Open(ctx, u.Query().Get("model"), creds)
   }
   ```

5. Add a test file with table-driven tests covering `Open` failures, representative message-to-plan mappings (including multi-message sequences when the backend keeps conversation context, and isolation across conversations and across connections), context cancellation, `Close` semantics, and opening through a `planner.Registry` with the exported scheme.
6. List the backend in the table above and document its URL parameters and credential identifiers in a section like the ping one.
