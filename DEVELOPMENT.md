# Developing chatops

This guide describes the internal packages and how to add credential stores, chat backends, tools, and planners. For installation, configuration, and command usage, see the [user guide](README.md).

## Engine (`engine`)

The `engine` package joins the component interfaces into the server loop. It receives messages from one `chat.Conn`, passes each message and its connection metadata to a `planner.Planner`, executes the returned tool steps in order, and sends every non-empty tool result back to the originating conversation. A planner asks for clarification or confirmation by returning a `reply://` step, so multi-message interaction state stays in the planner instead of the engine.

```go
e, err := engine.New(engine.Config{
    ConnectionID: "operations",
    Chat:         conn,
    Planner:      p,
    Tools:        tools,
    Credentials:  credentials,
})
if err != nil {
    // handle error
}
if err := e.Run(ctx); err != nil {
    // handle processing or cleanup error
}
```

`Run` preserves message order within each conversation while processing independent conversations concurrently through a fixed-size worker pool. The pool and its bounded backlog prevent messages from creating unbounded worker goroutines; `Config.MaxConcurrency` controls the worker count and defaults to `engine.DefaultMaxConcurrency`. The engine is deliberately fail-fast: a planner, tool step, or result-delivery failure stops the server and returns the error to its caller instead of continuing with potentially incomplete work. A panic from a planner or tool is recovered at the message boundary and returned as a processing error so cleanup can finish. Context cancellation and a connection deliberately closed through `chat.Conn.Close` are graceful outcomes. A remote disconnect such as telnet EOF is a connection failure and is returned, allowing the caller or service supervisor to decide whether to reconnect or restart.

The engine owns and closes the chat connection and planner after `New` succeeds, while the caller retains ownership of the credential store. It intentionally opens and closes each operational tool around one plan step, favoring isolated ownership and simple cleanup over engine-level instance reuse. A backend with expensive setup should implement safe pooling behind its opener rather than relying on the engine to retain stateful tool instances. Reply steps are different: the engine binds their destination to the originating conversation and accepts only the canonical `reply.URL`, preventing planner output from redirecting a reply or silently attaching unsupported URL configuration.

## Credential store (`cred`)

The `cred` package provides a generic way to access credentials from pluggable backends. The top-level package defines the interface; each backend lives in its own sub-package and exports the URL scheme it serves plus an opener, which callers wire into a registry (no `init()` side effects — supported backends are always visible at the wiring site):

```go
type Store interface {
    // Get retrieves the credential identified by key. It returns an
    // error wrapping cred.ErrNotFound when the key does not exist.
    Get(ctx context.Context, key string) (string, error)

    // Close releases any resources held by the store.
    Close() error
}
```

A store is identified by a single URL — the scheme selects the backend and the rest of the URL locates the store. Credentials for accessing the store itself are **never** part of the URL; each backend takes them from its standard environment variables (for example `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, `VAULT_TOKEN`), resolved through the backend SDK's default configuration chain.

Available backends:

| Scheme      | Sub-package     | Store URL                        |
| ----------- | --------------- | -------------------------------- |
| `json-file` | `cred/jsonfile` | `json-file:///path/to/file.json` |

How each backend lays out credentials in its store is backend-specific and documented per backend below.

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

secret, err := store.Get(context.Background(), "db-password")
if errors.Is(err, cred.ErrNotFound) {
    // credential does not exist
}
```

Backends also expose a typed `Open` function for direct use, e.g. `jsonfile.Open(ctx, "/etc/chatops/creds.json")`.

### json-file backend

The store URL is the file path (relative paths work too: `json-file://relative/path.json`). The file must contain a single JSON object mapping credential keys to string values:

```json
{
  "db-password": "hunter2",
  "api-token": "abc123"
}
```

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

A connection is identified by a single URL — the scheme selects the backend and the rest of the URL locates the server. Credential values are **never** part of the URL; backends resolve them from the `cred.Store` passed to `Registry.Open` under documented conventional key names. A backend that needs no credentials ignores the store.

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

The Slack backend uses the Events API and interactive payloads over Socket Mode for inbound messages, `chat.postMessage` for outbound replies, and `chat.update` to remove consumed controls. Its `slack://` URL takes no host, path, or query configuration. It resolves the bot OAuth token from `slack-bot-token` and the app-level token with `connections:write` from `slack-app-token`; see the user guide for the required app event subscriptions and bot scopes. Startup calls `auth.test` to validate the bot token and obtain its user ID before opening Socket Mode.

Every accepted Socket Mode envelope is acknowledged before its event is processed. Human message and `app_mention` events become `chat.Message` values only when their text starts with the exact `<@USERID>` obtained for the authenticated bot. The backend strips that stable bot identity before planning, so changing the bot's display name requires no ChatOps configuration. Mentions of other users, bot mentions later in the text, unmentioned messages, bot messages, message subtypes, events without a sender, and empty commands are ignored. The conversation ID combines the Slack channel ID with the root message timestamp. A root message and all replies in its thread therefore share one engine conversation, and `Send` posts back into that thread. Routing entries refresh on receive and send, expire after 24 hours of inactivity, and are limited to 4,096 entries. An indexed min-heap makes refresh, expiry, and capacity eviction O(log n); reaching capacity evicts the earliest-expiring route.

`chat.Message.Choices` carries optional label/value responses. The reply tool maps choices supplied on `tool.Call` into that backend-neutral field. Slack renders them as Block Kit buttons and treats a click as an ordinary inbound message containing the selected value; telnet ignores the metadata and sends the message's fallback text. Slack accepts only values registered for a prompt message posted by this process. Prompt routes expire after ten minutes, are capped at 4,096 entries, and are atomically removed on selection, which rejects expired, foreign, unregistered, and duplicate clicks without unbounded state. A valid click clears the message's buttons before delivery to the planner.

### telnet backend

The connection URL is the server address; the port defaults to the telnet port 23 (`telnet://chat.example.com` ≡ `telnet://chat.example.com:23`). The wire protocol is bare lines of text: every newline-terminated line received is one inbound message (blank lines are ignored), and `Send` writes the message text followed by a newline. Telnet option negotiation (IAC sequences) is not performed.

The connection carries a single conversation whose ID is the `telnet.ConversationID` constant; the protocol has no notion of identity, so `Message.Sender` is empty.

### Adding a new backend

1. Create a sub-package under `chat/` named after the backend (e.g. `chat/slack`).
2. Define a `Conn` type implementing the `chat.Conn` interface:
   - Compute `Message.ConversationID` on receive from the backend's native addressing (e.g. Slack channel + thread), and translate it back on send. Wrap `chat.ErrUnknownConversation` (with `%w`) when a sent ID does not map to a conversation.
   - After `Close`, `Receive` and `Send` report an error wrapping `chat.ErrClosed`; `Close` must also unblock a pending `Receive`.
3. Provide an `Open` function taking `context.Context`, a `cred.Store`, and any backend-specific location parameters, and returning `(*Conn, error)`. Resolve credential values from the store under documented conventional key names; never accept values as URL elements.
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

## Tools (`tool`)

The `tool` package provides a generic way to invoke operational tools (kubernetes, proxmox, harbor, a dummy ping tool, ...). The top-level package defines the interface; each tool lives in its own sub-package and exports the URL scheme it serves plus an opener, which callers wire into a registry (no `init()` side effects — supported tools are always visible at the wiring site):

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

A call carries enough detail for the tool to act, without prescribing how it maps to actual API calls or commands: an **action** (the verb, e.g. `restart`), a **target** (what it applies to, e.g. `deployment/web`; may be empty), and optional key-value **parameters**. The result carries **text** — the human-readable outcome, composed by the tool and ready to post to chat as-is — plus optional machine-readable key-value **details**; callers never need the details to render a reply. Text is empty only when the tool has already delivered the outcome to the human itself (like the reply tool, whose action is posting into chat), so callers relay non-empty text and stay silent on empty text.

A tool instance is identified by a single URL — the scheme selects the implementation, host/port/path locate the endpoint it operates on, and query parameters carry further instance configuration. Credential *values* are **never** part of the URL; tools resolve them from the `cred.Store` passed to `Open`. Each tool defines conventional key names prefixed by its name (e.g. `k8s-ca`/`k8s-cert`/`k8s-key`, `proxmox-ssh-user`/`proxmox-ssh-key`, `harbor-user`/`harbor-password`), and the prefix can be overridden per instance with the `cred-prefix` query parameter so multiple instances of the same tool can use distinct credentials:

```
kubernetes://prod.example.com:6443?cred-prefix=k8s-prod
```

Available tools:

| Scheme  | Sub-package   | Tool URL                        |
| ------- | ------------- | ------------------------------- |
| `ping`  | `tool/ping`   | `ping://`                       |
| `status` | `tool/status` | `status://`                     |
| `reply` | `tool/reply`  | `reply://` (no registry opener) |

### Usage

Build a registry from the tools you want, open the tool by URL with a credential store, then invoke actions:

```go
import (
    "context"
    "fmt"

    "github.com/hangxie/chatops/tool"
    "github.com/hangxie/chatops/tool/ping"
)

reg := tool.NewRegistry(
    tool.Backend{Scheme: ping.Scheme, Opener: ping.Opener, Descriptor: &ping.Descriptor},
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

Tools also expose a typed `Open` function for direct use, e.g. `ping.Open(ctx)`.

### ping tool

A dummy tool that answers `pong` to the `ping` action, useful as a liveness check and as the reference implementation of the interface. It has no endpoint and takes no credentials, so the URL is a bare `ping://` (anything beyond the scheme — host, path, query, userinfo, or non-empty fragment — is rejected; a bare trailing `#` parses identically to the bare URL and is accepted). The only supported action is `ping`; `Target` and `Parameters` are ignored, and any other action reports an error wrapping `tool.ErrUnknownAction`. It exports a `tool.Descriptor` (its single `ping` action) as a reference for the typed-schema wiring.

### status tool

The service-status tool checks public third-party status APIs and normalizes their different schemas. It has no credentials or caller-configurable endpoint, so its only URL is the bare `status://`; keeping upstream URLs in the compiled provider catalog prevents planner output from turning the tool into an arbitrary HTTP client.

The `check` action requires one canonical provider or alias in `Call.Target` and takes no parameters. The canonical providers are `github`, `anthropic`, `cloudflare`, `openai`, `gemini`, `slack`, and `docker-hub`; the special target `all` checks every canonical provider. The `list` action takes no target or parameters and returns the canonical provider names. See the user guide for the complete alias table. The tool exports a `tool.Descriptor` declaring both actions (with `check` taking a service-name target), so an LLM planner is offered `check`/`list` as an enum rather than guessing action names.

Providers use adapters for their public status platform: GitHub, Anthropic, Cloudflare, and OpenAI use the common Statuspage summary schema; Slack uses the Slack Status API; Gemini combines active incidents for the stable Vertex Gemini and Workspace Gemini product IDs from Google's public JSON feeds; and Docker Hub uses the Status.io public API. Health is normalized to `operational`, `maintenance`, `degraded`, `partial_outage`, `major_outage`, or `unknown`.

The checker preserves catalog order and limits aggregate checks to four concurrent provider requests. Network failures, non-success HTTP responses, and malformed upstream data become `unknown` snapshots so a status-page outage does not trigger the engine's fail-fast path; invalid tool calls and context cancellation are still returned as errors. Response bodies are bounded, and the shared HTTP client applies a five-second timeout.

When adding a provider, prefer an existing adapter and add aliases only when they are unambiguous. Public catalogs such as [awesome-status-pages](https://github.com/ivbeg/awesome-status-pages) can help identify candidates, but they are discovery aids rather than runtime dependencies: verify the provider's official page, machine-readable endpoint, response format, and continued availability before adding it to the compiled catalog. Add a new adapter only when no existing status platform schema fits, and cover its health mapping, incidents, malformed responses, and cancellation behavior with table-driven tests.

### reply tool

A tool that posts text back into a chat conversation, so a planner (see below) can express "say this to the requester" as an ordinary tool step alongside operational tool calls. Unlike other tools it is bound to a live `chat.Conn` — the connection the message being answered arrived on — rather than to an endpoint of its own, so it has **no `Opener`** and cannot be opened through a `tool.Registry`. Callers open it directly and make it available to plan execution under the conventional bare URL exported as `reply.URL` (`reply://`):

```go
import "github.com/hangxie/chatops/tool/reply"

rt, err := reply.Open(ctx, conn) // conn is the chat.Conn messages arrive on
```

The only supported action is `send`: `Target` is the conversation ID to post into (the `ConversationID` of the message being answered) and `Parameters["text"]` is the text to post. Sending is the whole outcome, so `Result.Text` stays empty — callers that post non-empty `Result.Text` back to chat will not double-post. The tool never closes the connection; that stays with the caller.

### Adding a new tool

1. Create a sub-package under `tool/` named after the tool (e.g. `tool/kubernetes`).
2. Define a `Tool` type implementing the `tool.Tool` interface:
   - `Invoke` maps the call's action/target/parameters onto the tool's API, wrapping `tool.ErrUnknownAction` (with `%w`) when the action is not supported so callers can detect it with `errors.Is`.
   - Compose `Result.Text` as the complete human-readable answer; put supplementary machine-readable output in `Result.Details`.
   - `Close` releases connections or other resources.
3. Provide an `Open` function taking `context.Context` plus tool-specific parameters and returning `(*Tool, error)`. Resolve credentials from the `cred.Store` using the tool's conventional key names (document them), honoring the `cred-prefix` override; never accept credential values as parameters or URL elements.
4. Export the scheme and an opener so callers can wire the tool into a `tool.Registry` (tools never self-register via `init()`):

   ```go
   // Scheme is the URL scheme this tool serves in a tool.Registry.
   const Scheme = "my-tool"

   // Opener is the tool.OpenerFunc for this tool.
   func Opener(ctx context.Context, u *url.URL, creds cred.Store) (tool.Tool, error) {
       return Open(ctx, u.Host, creds)
   }
   ```

5. Export a `tool.Descriptor` describing the tool and wire it into the `Backend` alongside the scheme and opener — it is required, so `NewRegistry` panics on a backend without one. The descriptor lets an LLM planner offer each of the tool's actions as its own typed function (named `<tool>-<action>`, each with its described `target` and typed parameters and their required fields) instead of making the model guess the vocabulary. Keep the described actions and parameters in step with `Invoke`.

   ```go
   // Descriptor is the tool's self-description for planners.
   var Descriptor = tool.Descriptor{
       Summary: "One-line, model-facing description of the tool.",
       Actions: []tool.Action{
           {Name: "restart", Description: "Restart the target.", TakesTarget: true, TargetDesc: "the deployment"},
       },
   }
   ```

   ```go
   tool.Backend{Scheme: mytool.Scheme, Opener: mytool.Opener, Descriptor: &mytool.Descriptor}
   ```

6. Add a test file with table-driven tests covering `Open` failures, supported and unknown actions, context cancellation, `Close` semantics, opening through a `tool.Registry` with the exported scheme, and that every described action is one `Invoke` accepts.
7. List the tool in the table above and document its actions and credential key names in a section like the ping one.

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

A request carries the message **text**, the **conversation ID** and **sender** (both as computed by the chat backend, see `chat.Message`), and a caller-assigned **connection ID**; planners use the connection and conversation IDs together to keep per-conversation context across requests. The connection ID exists because conversation IDs are only unique within one `chat.Conn` (every telnet connection reports the same one, for example): a caller serving several connections from one planner must give each connection a distinct opaque ID, while a caller with a single connection may leave it empty. The returned plan is a sequence of **steps**, each naming a tool by the URL it is opened from (see the `tool` package) plus the `tool.Call` to invoke on it. Replying to the requester is itself a step — one invoking the `reply://` tool — so a clarifying question and an operational action have the same shape, mirroring how LLM tool-use APIs treat text output and tool calls as peers in one turn.

Steps name tools by URL only, so a plan is **not self-contained**: the caller executes it in the context of the request that produced it. In particular, `reply://` resolves to the reply tool bound to the chat connection that request arrived on — a caller serving several connections keeps one reply tool per connection rather than sharing one — which is what keeps replies on the right connection even when conversation IDs collide across connections.

A planner is identified by a single URL — the scheme selects the backend, host/port/path locate the endpoint it talks to (empty for providers with a well-known API endpoint), and query parameters carry further configuration such as the model (e.g. `openai-chat-completions://api.openai.com/v1?model=gpt-5`, `anthropic://?model=claude-fable-5`). As with `tool`, credential *values* are **never** part of the URL; backends resolve them (e.g. API keys) from the `cred.Store` passed to `Open`, under conventional key names prefixed by the backend name (e.g. `anthropic-api-key`), or a `cred-prefix` query parameter.

`Open` also receives the caller's enabled tool set (the `*tool.Registry` built from `--tool`), so an LLM-backed backend can offer those tools to the model as callable functions and emit plan steps naming them by scheme. A backend that plans a fixed set of steps (such as `ping`) ignores it.

**Breaking change (tool set threaded through `Open`).** Adding the OpenAI backend required the enabled tool set to reach planners, so `planner.OpenerFunc` and `planner.Registry.Open` each gained a trailing `tools *tool.Registry` argument. To migrate: callers pass the enabled registry (or `nil`, treated as empty) as the new final argument to `Open`; backend `OpenerFunc` implementations add the trailing `tools *tool.Registry` parameter and ignore it unless they offer tools to a model. There is no compatibility shim — the parameter is mandatory — because the planner interface is still pre-1.0 and has no external backends.

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
// tools is the enabled *tool.Registry; nil is treated as the empty set.
// creds and tools are passed through to the backend's opener.
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
for _, step := range plan.Steps {
    // resolve step.Tool ("ping://", "reply://", ...) to an opened
    // tool.Tool — "reply://" to the reply tool bound to the
    // connection msg arrived on — invoke step.Call on it, and post
    // any non-empty Result.Text back into the conversation
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

A planner backed by any service that speaks the OpenAI Chat Completions API, so the same backend drives OpenAI, Google Gemini's OpenAI-compatible endpoint, a local Ollama, vLLM, LocalAI, and similar servers. The endpoint is configured through the URL: the host is required (the planner is not tied to a fixed provider) and locates the endpoint, whose path defaults to `/v1`, with `insecure=true` selecting plain HTTP. The `model` query parameter is required (there is no universal default across services). The API key is read from the `cred.Store` under `<cred-prefix>-api-key` when `cred-prefix` is set and sent as a bearer token; without it, or when no key is found, no `Authorization` header is sent, so keyless servers work.

- The host is required, so a hostless or mistyped URL (e.g. the typo `openai-chat-completions:///host/v1` with three slashes, which parses to an empty host) is rejected rather than silently defaulting to some provider.
- Each enabled tool's scheme is offered to the model as a function name, so the schemes must satisfy the OpenAI function-name rules (letters, digits, `_`, `-`, up to 64 characters). A tool whose scheme uses `+` or `.` is rejected when the planner is opened, rather than making every completion request fail.
- On each message the planner makes one Chat Completions request, offering the enabled operational tools (from the tool set passed to `Open`) plus a built-in `reply` function. Tools are offered generically: each is a function taking an `action`, an optional `target`, and optional string `parameters`, mirroring `tool.Call`.
- The model's response maps to plan steps: assistant prose and each `reply` call become `reply://` steps, and each operational tool call becomes a step invoking that tool by its `<scheme>://` URL.
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
   - `Plan` turns one inbound message into steps; express replies and clarifying questions as steps invoking the `reply://` tool with the request's `ConversationID` as the call target. Keep any per-conversation context keyed by the `(ConnectionID, ConversationID)` pair — never by `ConversationID` alone, which collides across chat connections — and make the planner safe for concurrent use.
   - `Close` releases connections or other resources.
3. Provide an `Open` function taking `context.Context` plus backend-specific parameters and returning `(*Planner, error)`. Resolve credentials (e.g. API keys) from the `cred.Store` using the backend's conventional key names (document them), honoring the `cred-prefix` override; never accept credential values as parameters or URL elements.
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
6. List the backend in the table above and document its URL parameters and credential key names in a section like the ping one.
