# chat backends

Status: accepted design; `slack`, `discord`, `stdio`, and `unix` implemented, others planned ([`DESIGN.md`](DESIGN.md) chat adapter).

A chat backend is one way users reach the agent: an interactive terminal, Slack, Discord, a Unix socket, a TCP socket, or an HTTP endpoint.

- Each backend delivers inbound messages and posts replies.
- The agent, conversation, and tool layers are backend-agnostic.
- The daemon runs one or more named backends at once, all feeding a single conversation manager.
- A new platform is a new backend, not a new code path.

## Backends behind one interface

A backend satisfies one interface:

    // internal/chat
    type Backend interface {
        Run(ctx context.Context, submit func(conversation.Message) error) error
        Connected() bool
    }

A message makes a round trip through the backend that received it:

    platform ──▶ Backend.Run ──submit(Message)──▶ conversation manager
       ▲                                                  │
       └─────────────── Message.Reply(Reply) ◀────────────┘

**`Run`** delivers each inbound message by calling `submit`.

**`Reply`** is the entire platform abstraction:

- Every `conversation.Message` carries a `Reply` closure that posts back to the message's own thread, connection, or stream.
- A `conversation.Reply` holds the answer `Text` and an optional stats `Footer` (see [`observability.md`](observability.md)).
- The two are kept apart so a backend can style the footer its platform's way.
- A backend without rich formatting sends `Plain()`: the footer after a blank line.

**`Connected`** reports whether the backend can receive messages right now. It feeds readiness and the `chatops_chat_backend_up` gauge.

| Backend | Connected while |
|---|---|
| `slack` | between Socket Mode's connected and disconnect events |
| `discord` | between the gateway's ready (or resumed) and disconnect events |
| `unix` | listening |
| `stdio` | reading |

**Wiring:**

- A `New(name, cfg, logger) (Backend, error)` factory builds each backend, switching on the configured `type`.
- There is no `init()` or blank-import registration.
- `stdio` is an ordinary backend: selected, configured, validated, and namespaced like every other. It is never a default or fallback.

## Selecting backends

`chatops serve --chat <names>` runs the daemon against the named backends.

- `--chat` is required, comma-separated, and has no default.
- Startup aborts before connecting anything if a named backend is absent from the config or invalid.

## Configuration

`chat:` maps a name you choose to a backend: its `type` plus that type's fields. Because each backend has its own name, you can run several of the same type, such as two Slack workspaces:

    chat:
      eng-slack:
        type: slack
        bot_token_env: ENG_SLACK_BOT_TOKEN
        app_token_env: ENG_SLACK_APP_TOKEN
      console:
        type: stdio
      public-api:
        type: http
        listen: 0.0.0.0:8080
        bearer_token_env: CHAT_HTTP_TOKEN
      ops-tcp:
        type: tcp
        listen: 127.0.0.1:4000

| Check | Runs for |
|---|---|
| Structural validation (known `type`, well-formed fields) | every entry, at config load |
| Secret resolution and live connection | only the backends named in `--chat` |

## Backend types

Implementation status, updated as the work merges:

| Backend | Status | Conversation | Access control |
|---|---|---|---|
| `stdio` | implemented | one, on stdin | the session |
| `slack` | implemented | one per Slack thread | Slack workspace, verified user ID |
| `discord` | implemented | one per message, or one per Discord thread | Discord server, verified user ID |
| `unix` | implemented | one per connection | socket file permissions |
| `http` | planned | caller-supplied key | bearer token off loopback |
| `tcp` | planned | one per connection | loopback only, no auth |
| `udp` | deferred | | |

### `stdio`

An interactive terminal: reads lines from stdin, writes replies to stdout, and exits on EOF or `/quit`.

- It runs on the shared conversation manager like every other backend.
- It needs a terminal, so it is for development and local testing only.
- Under an unattended daemon (systemd), stdin is EOF and the backend exits at once. It is never a way to reach a running daemon.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `stdio` |

### `slack`

Slack Socket Mode, one conversation per Slack thread. Sender identity is the verified Slack user ID from the event.

Formatting:

- Replies post into the thread as escaped mrkdwn, without link previews. With `reply_mode: channel`, a top-level message is answered in its channel or DM instead.
- Common Markdown the model emits is converted to mrkdwn.
- A reply with a Markdown table is posted as Block Kit blocks. The first table that fits a native Slack table block is posted as one: Slack allows one per message, up to 100 rows, 20 columns, and 10,000 characters, with no empty cells.
- Every other table becomes an aligned code block.
- A table ends at the first row whose column count differs from its header.

Length and fallback:

- A block reply is not cut at the plain-text length cap. It is limited to Slack's 50 blocks instead, ending with "[reply truncated]" if longer.
- A stats footer is posted below a divider as a context block (smaller grey text) and left out of the notification text.
- If Slack rejects the blocks, the reply is resent as plain mrkdwn, footer included.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `slack` |
| `bot_token_env` | yes | Environment variable holding the bot token (`xoxb-…`) |
| `app_token_env` | yes | Environment variable holding the app-level token (`xapp-…`) for Socket Mode |
| `reply_mode` | no | `thread` (default) or `channel`: where a top-level message is answered |

### `discord`

The Discord Gateway: a websocket the bot opens outbound, like Slack Socket Mode, so it needs no public endpoint. Discord's webhook-based Interactions endpoint is not used.

Intents and filtering:

- The bot requests `Guilds`, `Guild Messages`, and `Direct Messages`, but not the privileged Message Content intent. Discord still delivers the content of messages that mention the bot and of direct messages, the only ones it answers.
- A channel message must start with a mention of the bot; a direct message needs none.
- Messages from bots and webhooks, and system messages, are ignored.

Conversations and identity:

- Each message is its own conversation, except inside a Discord thread, where the thread is one conversation.
- A thread is recognized from the gateway's channel cache, or by a REST lookup when a revived thread is not cached yet.
- A conversation is keyed by the thread ID inside a thread and by the message ID elsewhere. Discord gives a thread started from a message that message's ID, so a top-level question and the thread opened on it share one key and one history.
- Sender identity is the Discord user ID from the gateway event.

Thread mode (`reply_mode: thread`):

- A top-level channel message is answered in a public thread the bot starts on it. Messages already in a thread and direct messages, which cannot have threads, are answered as in channel mode.
- The thread is started when the first reply is sent, off the gateway event loop, so a slow REST call never delays other messages.
- The thread is named after the question's first line, cut to Discord's 100 characters, and archives after a day of inactivity.
- If a thread already exists on the message (Discord error 160004), the reply goes into it, since its ID is the message ID.
- Any other failure, such as a missing Create Public Threads permission or a channel type without threads, falls back to a channel reply and logs a warning.
- Follow-ups in the thread still need a mention, because without the Message Content intent Discord sends the bot no text for other messages.

Replies:

- Replies ping no one (no allowed mentions, not even the replied-to user) and suppress embeds, so links do not unfurl.
- Outside a new thread, the first message is a Discord reply to the user's message. In a thread the bot started, it is a plain message below the question.
- Discord renders Markdown, so the answer is sent as written, except tables, which Discord does not render and which become aligned code blocks.
- A reply is split at line breaks into messages of at most 2,000 characters. A code block spanning two messages is closed and reopened with its language.
- A reply is cut after five messages with "[reply truncated]".
- A stats footer is sent as subtext (`-#` lines, smaller grey text) below the answer, in a message of its own when the last one has no room.

Connection: the gateway reconnects and resumes on its own; a token Discord rejects fails startup.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `discord` |
| `bot_token_env` | yes | Environment variable holding the bot token |
| `reply_mode` | no | `channel` (default) or `thread`: where a top-level channel message is answered |

### `unix`

A Unix domain socket for a client on the same host (`nc -U`, `socat`) to reach a running daemon. One conversation per connection, a message per line, replies written back to the connection.

Access and identity:

- The socket file's permissions control which local processes may connect, so restrict its owner and group. There is no in-band token.
- Those permissions gate access but do not identify the caller (several users in one group may be allowed). Until Phase 4 adds peer-credential inspection (Linux `SO_PEERCRED`) to populate `User`, the sender is the connection.

Connections:

- Each connection is a fresh conversation, keyed by a unique per-connection ID.
- One message is answered at a time per connection.
- A disconnect ends that connection; a reply still in flight is dropped when its write fails.
- Replies are written under a deadline, so a client that stops reading cannot stall the connection or shutdown.

Socket file lifecycle:

- On startup, a stale socket from an earlier run is cleared only after a connect probe shows nothing is listening. A live socket refuses startup rather than being unlinked.
- An exclusive lock file next to the socket (`<path>.lock`), held for the listener's lifetime, serializes the probe/remove/bind sequence. A second daemon on the same path fails fast.
- Closing the listener removes the socket on exit.

The reply protocol is one line of text, for interactive clients. Programmatic framing for multi-line replies (length-prefix or JSON Lines) is future work.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `unix` |
| `path` | yes | Filesystem path of the socket to create |
| `mode` | no | Socket file permission bits; defaults to owner-only |

### `http`

A request/response HTTP endpoint, and the transport the platform-adapter backends build on.

- The reply is returned on the request.
- One conversation per caller-supplied conversation key, within the single principal the shared bearer token represents.
- A shared token does not isolate that application's own users, so the endpoint is one trusted client.
- A non-loopback `listen` without a bearer token is refused at startup (the shared `checkBind` rule). When a token is set, every request must present it.
- TLS is a fronting proxy's job.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `http` |
| `listen` | yes | Bind address `host:port` |
| `bearer_token_env` | conditional | Environment variable holding the bearer token; mandatory unless `listen` is loopback |
| `path` | no | Request path; defaults to `/chat` |

### `tcp`

A connection-oriented network socket for a line-oriented client (`telnet`, `nc`). One conversation per connection, a message per line, replies written back.

- Each conversation is keyed by a unique per-connection ID, not the remote address.
- Writes per connection are serialized, and a disconnect cancels the in-flight turn.
- It binds loopback only and refuses any non-loopback `listen`, because a network socket has no filesystem permissions to authorize callers, and it speaks no authentication.
- Remote access is an operator's `ssh -L` tunnel to localhost. Native remote auth (TLS with client certificates) is out of scope.

It is kept separate from `unix` on purpose: only a filesystem socket can carry permission-based access control.

UDP is not a variant of this backend. It is connectionless, so it needs its own session identity, expiry, datagram-size, and reply-routing design, and is deferred (see Future work).

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `tcp` |
| `listen` | yes | Bind address `host:port`; must be loopback |

## Runtime

`serve` builds the model, the MCP tool clients, and one `conversation.Manager`, then runs the selected backends concurrently under an error group, each calling the manager's `submit`.

The runner wraps each backend's `submit` to prefix the conversation key with the backend name: `eng-slack:<channel-ts>`, `console:stdin`, `ops-tcp:conn-7`. Conversations from different backends therefore cannot collide in history or ordering, and logs and audit records name the originating backend.

## Message lifecycle

**`submit` accepts, it does not complete.** It queues the message and returns at once, never blocking on the agent:

| Return | When |
|---|---|
| `nil` | accepted |
| `ErrBusy` | `max_pending_messages` are in flight |
| `ErrClosed` | during shutdown |

**`ErrBusy` maps to a platform response:**

| Backend | Busy response |
|---|---|
| Slack, Discord | a brief busy message |
| HTTP | `429` or `503` |
| TCP | a short busy line |

**`Reply` is asynchronous.**

- The manager invokes it exactly once after the turn finishes, with the turn's text or a generic failure line, under a reply-delivery timeout.
- A failed `Reply` is logged, not retried, and does not fail the turn.
- Each backend keeps its delivery path alive until `Reply` runs: the HTTP handler blocks on the reply within the request; a TCP connection stays open.
- Replies for different conversations may run at the same time, so a backend sharing one output destination synchronizes its writes (`stdio` guards stdout, socket backends serialize per connection).

**Ordering.** Within one conversation, messages are processed and replied strictly in order. Independent conversations run concurrently up to `max_concurrent_turns`. A concurrency slot is held only while the agent works, not while a reply is delivered.

**Shutdown is graceful.**

1. The daemon stops accepting messages (`submit` returns `ErrClosed`).
2. In-flight turns and their replies get a bounded grace period (default 30 seconds) covering both agent execution and reply delivery.
3. Anything still running is cancelled.

Reply delivery is detached from turn cancellation, so an already-accepted message still receives its answer.

## Conversation key versus sender identity

| Concept | Field | Purpose |
|---|---|---|
| Conversation key | `Conversation` | History and ordering only; prefixed with the backend name |
| Verified sender identity | `User` | Audit now, Phase 4 authorization later |

A conversation key is never treated as identity. `User` is the authenticated principal the backend vouches for, and authorization keys on it, never on message text or the conversation key.

| Backend | `User` |
|---|---|
| `slack` | verified Slack user ID |
| `discord` | Discord user ID from the gateway event |
| `http` | the shared bearer token, one principal; per-user isolation is the upstream application's job until distinct identities exist |
| `unix` | none yet; file permissions are not a caller identity. Phase 4 would use `SO_PEERCRED` peer credentials |
| `tcp` | none; treated as untrusted-but-local |

## Backend lifecycle

The conversation manager is owned by `serve`, not by any backend.

| Event | Effect |
|---|---|
| Startup failure | fails the command before any backend starts |
| A backend returns `nil` | finishes only itself; the shared manager stays open (`stdio` reaching EOF leaves other backends running) |
| Every backend has returned | the command exits after draining accepted turns and replies within the grace period |
| A backend returns an error | cancels the group and fails the command |
| Transient disconnect (Slack, Discord) | reconnected internally; an error is returned only on unrecoverable failure |

When a backend fails fatally, replies still in flight are delivered best-effort:

- The failing backend's own transport may already be gone.
- Shared clients that outlive a backend's `Run`, such as the Slack Web API client, can still post during the drain.
- Each backend keeps waiting for an accepted message's reply through the drain window, rather than abandoning it the moment its context is cancelled.

## Package layout

| Package | Holds |
|---|---|
| `internal/chat` | the `Backend` interface, the `New` factory, and the multi-backend runner |
| `internal/chat/<type>` | one backend each: `stdio`, `slack`, `discord`, `unix`, later `http` and `tcp` |
| `internal/chat/markdown` | Markdown table parsing shared by the platform backends |
| `cmd/chatops` | a single `serve` command |

Every source file has a one-to-one `_test.go`.

## Future work

- Teams, Webex, and SMS: webhook-based platform-adapter backends that reuse a shared HTTP listener and request-handling utilities, not configuration variants of the generic `http` backend.
- Per-user authorization (Phase 4), applied uniformly across backends, keyed on each backend's verified sender identity.

Deferred:

- A request-ID/retrieval mode for `http`; the accepted design is request/response only.
- A `udp` backend. Being connectionless, it needs its own session-identity, expiry, datagram-size, and reply-routing design, and will not be added without a concrete use case.
- Native TLS client-certificate auth for remote sockets.

## Decisions

1. **One interface, explicit wiring.** Every backend is a message source behind the one `Backend` interface, wired through a `type`-switched factory (no `init()` or blank import). A message's `Reply` closure is the entire platform abstraction.
2. **Named configuration, required selection.** Backends are a named `chat:` map keyed by a `type` discriminator, with multiple instances per type. `--chat` selects them, required with no default, and startup aborts on a selected backend that is missing or invalid.
3. **One shared manager, isolated namespaces.** All backends feed one `serve`-owned conversation manager; keys are prefixed with the backend name so histories and ordering never collide.
4. **Asynchronous, ordered, graceful.** `submit` never blocks; `Reply` fires exactly once and is concurrency-safe; per-conversation order is preserved; overload is central (`ErrBusy`, mapped per backend); shutdown drains within a bounded grace period.
5. **Sender identity is separate from conversation identity.** A key is never treated as identity, and the `http` shared-token principal does not isolate an application's own users.
6. **Per-backend security.** Each backend authorizes its own way (see [Backend types](#backend-types)). `unix` stays separate from `tcp` because only a filesystem socket can carry permission-based access control.
7. **Adapter extensibility.** New webhook platforms (Teams, Webex, SMS) are adapter backends reusing the shared HTTP listener. A platform with an outbound connection, as Slack Socket Mode and the Discord Gateway are, uses it instead and needs no public endpoint.
