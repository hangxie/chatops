# chat backends

Status: accepted design, not yet implemented ([`DESIGN.md`](DESIGN.md) chat adapter).

A chat backend is one way users reach the agent. Slack, a local terminal, an HTTP endpoint, and a raw TCP socket are all backends: each delivers inbound messages and posts replies, and the agent, conversation, and tool layers are backend-agnostic. The daemon runs one or more named backends at once, all feeding a single conversation manager; a new platform is a new backend, not a new code path.

## Backends behind one interface

A backend satisfies one interface:

    // internal/chat
    type Backend interface {
        Run(ctx context.Context, submit func(conversation.Message) error) error
    }

`Run` delivers each inbound message by calling `submit`; every `conversation.Message` carries a `Reply` closure that posts text back to its own thread, connection, or stream, and that closure is the entire platform abstraction. Backends are wired explicitly through a `New(name, cfg, logger) (Backend, error)` factory that switches on the configured `type`; there is no `init()` or blank-import registration. `local` is an ordinary backend: selected, configured, validated, and namespaced like every other, and never a default or fallback.

## Selecting backends

`chatops serve --chat <names>` runs the daemon against the named backends. `--chat` is required, comma-separated, and has no default. There is one `serve` command and no separate `chat` command; the terminal is a `local` backend. Startup aborts before connecting anything if a named backend is absent from the config or its configuration is invalid.

## Configuration

`chat:` is a map from a free-form name to a backend definition. Each definition has a `type` discriminator plus that type's fields, mirroring `mcp.servers`, so multiple instances of one type coexist under different names:

    chat:
      eng-slack:
        type: slack
        bot_token_env: ENG_SLACK_BOT_TOKEN
        app_token_env: ENG_SLACK_APP_TOKEN
      console:
        type: local
      public-api:
        type: http
        listen: 0.0.0.0:8080
        bearer_token_env: CHAT_HTTP_TOKEN
      ops-tcp:
        type: tcp
        listen: 127.0.0.1:4000

Structural validation (known `type`, well-formed fields) runs for every entry at config load. Secret resolution and live connection happen only for the backends named in `--chat`.

## Backend types

Implementation status, updated as the work merges:

| Backend | Status |
|---|---|
| `slack` | planned |
| `local` | planned |
| `http` | planned |
| `tcp` | planned |

### `slack`

Slack Socket Mode. One conversation per Slack thread; replies post into the thread as escaped mrkdwn without link previews. Sender identity is the verified Slack user ID from the event.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `slack` |
| `bot_token_env` | yes | Environment variable holding the bot token (`xoxb-…`) |
| `app_token_env` | yes | Environment variable holding the app-level token (`xapp-…`) for Socket Mode |

### `local`

A single-conversation terminal: reads lines from stdin, writes replies to stdout, and exits on EOF or `/quit`. It runs on the shared conversation manager like every other backend.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `local` |

### `http`

A request/response HTTP endpoint, and the transport the platform-adapter backends build on. One conversation per caller-supplied conversation key within the single principal the shared bearer token represents; a shared token does not isolate that application's own users, so the endpoint is one trusted client. The reply is returned on the request.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `http` |
| `listen` | yes | Bind address `host:port` |
| `bearer_token_env` | conditional | Environment variable holding the bearer token; mandatory unless `listen` is loopback |
| `path` | no | Request path; defaults to `/chat` |

A non-loopback `listen` without a bearer token is refused at startup (the shared `checkBind` rule); when a token is set, every request must present it. TLS is a fronting proxy's job.

### `tcp`

A raw line-oriented socket for `telnet`/`nc`: one conversation per connection, a message per line, replies written back to the connection. Each connection is a fresh conversation keyed by a unique per-connection ID, not the remote address; writes per connection are serialized, and a disconnect cancels the in-flight turn.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `tcp` |
| `listen` | yes | Bind address `host:port`; must be loopback |
| `protocol` | no | `tcp` (default); `udp` is a possible later addition |

The socket speaks no authentication, so the backend binds loopback only and refuses any non-loopback `listen`. Remote access is an operator's SSH tunnel to localhost; native remote TCP auth (TLS with client certificates) is out of scope.

## Runtime

`serve` builds the model, the MCP tool clients, and one `conversation.Manager`, then runs the selected backends concurrently under an error group, each calling the manager's `submit`. The runner wraps each backend's `submit` to prefix the conversation key with the backend name (`eng-slack:<channel-ts>`, `console:stdin`, `ops-tcp:conn-7`), so conversations from different backends cannot collide in history or ordering, and logs and audit records name the originating backend.

## Message lifecycle

- `submit` is accept, not complete: it queues the message and returns at once — `nil` accepted, `ErrBusy` when `max_pending_messages` are in flight, `ErrClosed` during shutdown — and never blocks on the agent.
- `Reply` is asynchronous: the manager invokes it exactly once after the turn finishes (the turn's text, or a generic failure line), under a reply-delivery timeout. A failed `Reply` is logged, not retried, and does not fail the turn. Each backend keeps its delivery path alive until `Reply` runs: the HTTP handler blocks on the reply within the request; a TCP connection stays open.
- Ordering: within one conversation, messages are processed and replied strictly in order; independent conversations run concurrently up to `max_concurrent_turns`, and a concurrency slot is held only while the agent works, not while a reply is delivered.
- Overload: `submit` returns `ErrBusy`, which each backend maps to a platform response — Slack a brief busy message, HTTP `429`/`503`, TCP a short busy line.
- Reply concurrency: replies for different conversations may run at the same time, so a backend sharing one output destination synchronizes its writes (`local` guards stdout, `tcp` serializes per connection).
- Shutdown is graceful: the daemon stops accepting messages (`submit` returns `ErrClosed`), lets in-flight turns and their replies finish within a bounded grace period (default 30 seconds) covering both agent execution and reply delivery, then cancels anything still running. Reply delivery is detached from turn cancellation, so an already-accepted message still receives its answer.

## Conversation key versus sender identity

| Concept | Field | Purpose |
|---|---|---|
| Conversation key | `Conversation` | History and ordering only; prefixed with the backend name |
| Verified sender identity | `User` | Audit now, Phase 4 authorization later |

A conversation key is never treated as identity. `User` is the authenticated principal the backend vouches for, and authorization keys on it, not on message text or the conversation key. Slack supplies the verified Slack user ID. The `http` backend's shared bearer token is one principal and does not isolate the application's own users, so per-user isolation is the upstream application's responsibility until distinct authenticated identities exist. `tcp` has no authenticated identity and is treated as untrusted-but-local.

## Backend lifecycle

The conversation manager is owned by `serve`, not by any backend. A backend returning `nil` finishes only itself and never closes the shared manager, so `local` reaching EOF leaves the other backends running; the command exits once every selected backend has returned, after draining already-accepted turns and their replies within the grace period. A backend returning a non-nil error cancels the group and fails the command. Startup failures fail the command before any backend starts. Slack reconnects transient disconnects internally and returns an error only on unrecoverable failure.

## Package layout

`internal/chat` holds the `Backend` interface, the `New` factory, and the multi-backend runner; each backend implementation lives beside it in its own package (`internal/chat/slack`, `internal/chat/local`, then `internal/chat/http` and `internal/chat/tcp`), each with a one-to-one `_test.go`. `cmd/chatops` keeps a single `serve` command.

## Future work

Discord, Teams, Webex, and SMS are webhook-based platform-adapter backends that reuse a shared HTTP listener and request-handling utilities, not configuration variants of the generic `http` backend. Per-user authorization (Phase 4) applies uniformly across backends, keyed on each backend's verified sender identity. Deferred: a request-ID/retrieval mode for `http` (the accepted design is request/response only), `udp` for the `tcp` backend, and native TLS client-certificate auth for remote sockets.

## Decisions

1. **One interface, explicit wiring.** Every backend is a message source behind the one `Backend` interface, wired through a `type`-switched factory (no `init()` or blank-import); the agent, conversation, and tool layers stay backend-agnostic, and a message's `Reply` closure is the entire platform abstraction.
2. **Named configuration, required selection.** Backends are a named `chat:` map keyed by a `type` discriminator (mirroring `mcp.servers`, multiple instances per type); `chatops serve --chat <names>` selects them, required with no default and no standalone `chat` command, and startup aborts on a selected backend that is missing or invalid.
3. **One shared manager, isolated namespaces.** All backends feed a single `serve`-owned conversation manager; conversation keys are prefixed with the backend name so histories and ordering never collide across backends.
4. **Asynchronous lifecycle, ordered, graceful.** `submit` accepts without blocking (or returns `ErrBusy`/`ErrClosed`); `Reply` fires exactly once after the turn and must be concurrency-safe across conversations; per-conversation order is preserved while independent conversations run concurrently; overload is central (`ErrBusy`, mapped per backend); shutdown is graceful within a bounded grace period.
5. **Sender identity is separate from conversation identity.** Conversation key (history and ordering) and verified sender identity (`User`, for audit and Phase 4 authorization) are distinct fields; a key is never treated as identity, and the `http` shared-token principal does not isolate an application's own users.
6. **Per-backend security, adapter extensibility.** `http` requires a bearer token off loopback (the shared `checkBind` rule) and `tcp` is loopback-only; new platforms (Discord, Teams, Webex, SMS) extend the model as adapter backends reusing the shared HTTP listener, not as new code paths or `http` configuration variants.
