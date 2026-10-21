# chat backends

Status: accepted design; `slack`, `discord`, `stdio`, and `unix` implemented, others planned ([`DESIGN.md`](DESIGN.md) chat adapter).

A chat backend is one way users reach the agent: an interactive terminal, Slack, Discord, a Unix socket, or an HTTP endpoint.

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
- `stdio` is an ordinary backend: configured, validated, and namespaced like every other. It is never a default or fallback.

## Selecting backends

`chatops serve` runs every backend under `chat:`. The config file is the only switch.

- To turn a backend off, comment it out or remove it. There is no command-line flag or environment variable that selects backends.
- Startup aborts if `chat:` is empty, or before connecting anything if any backend is invalid.
- `stdio` reads the terminal, so a config used under systemd must not define one: the backend hits EOF at once and exits.

## Configuration

`chat:` maps a name you choose to a backend: its `type` plus that type's fields. Each backend has its own name, so you can run several of the same type, such as two Slack workspaces:

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

| Check | Runs for |
|---|---|
| Structural validation (known `type`, well-formed fields) | every entry, at config load |
| Secret resolution and live connection | every entry, at startup |

## Backend types

Implementation status, updated as the work merges:

| Backend | Status | Conversation | Access control |
|---|---|---|---|
| `stdio` | implemented | one, on stdin | the session |
| `slack` | implemented | one per Slack thread | Slack workspace, verified user ID |
| `discord` | implemented | one per message, or one per Discord thread | Discord server, verified user ID |
| `unix` | implemented | one per connection | socket file permissions |
| `http` | planned | caller-supplied key | bearer token, always |

Every backend authenticates who connects. `tcp` and `udp` are dropped for that reason: with no authentication, anyone who could connect could do anything.

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
- A reply with a Markdown table is posted as Block Kit blocks.
- The first table that fits a native Slack table block is posted as one. Slack allows one per message, up to 100 rows, 20 columns, and 10,000 characters, with no empty cells.
- Every other table becomes an aligned code block.
- A table ends at the first row whose column count differs from its header.

Length and fallback:

- A block reply is not cut at the plain-text length cap. It is limited to Slack's 50 blocks instead, and ends with "[reply truncated]" if longer.
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

- The bot requests `Guilds`, `Guild Messages`, and `Direct Messages`, but not the privileged Message Content intent.
- Discord still delivers the content of messages that mention the bot, and of direct messages. Those are the only ones it answers.
- A channel message must start with a mention of the bot; a direct message needs none.
- Messages from bots and webhooks, and system messages, are ignored.

Conversations and identity:

- Each message is its own conversation, except inside a Discord thread, where the thread is one conversation.
- A thread is recognized from the gateway's channel cache, or by a REST lookup when a revived thread is not cached yet.
- A conversation is keyed by the thread ID inside a thread, and by the message ID elsewhere.
- A thread started from a message gets that message's ID. So a top-level question and the thread opened on it share one key and one history.
- Sender identity is the Discord user ID from the gateway event.

Thread mode (`reply_mode: thread`):

- A top-level channel message is answered in a public thread the bot starts on it.
- Messages already in a thread, and direct messages (which cannot have threads), are answered as in channel mode.
- The thread is started when the first reply is sent, off the gateway event loop. A slow REST call never delays other messages.
- The thread is named after the question's first line, cut to Discord's 100 characters, and archives after a day of inactivity.
- If a thread already exists on the message (Discord error 160004), the reply goes into it, since its ID is the message ID.
- Any other failure falls back to a channel reply and logs a warning. Examples: a missing Create Public Threads permission, or a channel type without threads.
- The first attempt decides where the whole answer goes. A transient failure is not retried for later parts, so one answer never splits between the channel and a thread.
- A busy reply stays in the channel, so an overloaded bot opens no thread that never gets an answer.
- Follow-ups in the thread still need a mention, because without the Message Content intent Discord sends the bot no text for other messages.

Replies:

- Replies ping no one (no allowed mentions, not even the replied-to user) and suppress embeds, so links do not unfurl.
- Outside a new thread, the first message is a Discord reply to the user's message. In a thread the bot started, it is a plain message below the question.
- Discord renders Markdown, so the answer is sent as written. The exception is tables: Discord does not render them, so they become aligned code blocks.
- A reply is split at line breaks into messages of at most 2,000 characters. A code block spanning two messages is closed and reopened with its language.
- A reply is cut after five messages with "[reply truncated]".
- A stats footer is sent as subtext (`-#` lines, smaller grey text) below the answer. It gets a message of its own when the last one has no room.

Connection:

- The gateway reconnects and resumes on its own.
- A token Discord rejects fails startup.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `discord` |
| `bot_token_env` | yes | Environment variable holding the bot token |
| `reply_mode` | no | `channel` (default) or `thread`: where a top-level channel message is answered |

### `unix`

A Unix domain socket, for a client on the same host (`nc -U`, `socat`) to reach a running daemon.

- One conversation per connection.
- One message per line, with replies written back to the connection.

Access and identity:

- The socket file's permissions control which local processes may connect, so restrict its owner and group. There is no in-band token.
- Those permissions gate access, but do not identify the caller: several users in one group may be allowed.
- Connecting grants nothing on its own. Socket users follow roles and rules like everyone else ([`policy.md`](policy.md#local-users)).
- Until Phase 4, the sender is the connection. Phase 4 sets `User` to the peer's numeric user ID from the peer credentials (`SO_PEERCRED` on Linux, `getpeereid` on macOS), so several SREs on one socket are told apart ([`policy.md`](policy.md#principals)).

Connections:

- Each connection is a fresh conversation, keyed by a unique per-connection ID.
- One message is answered at a time per connection.
- A disconnect ends that connection; a reply still in flight is dropped when its write fails.
- Replies are written under a deadline, so a client that stops reading cannot stall the connection or shutdown.

Socket file lifecycle:

- On startup, a stale socket from an earlier run is cleared only after a connect probe shows nothing is listening.
- A live socket refuses startup, rather than being unlinked.
- An exclusive lock file next to the socket (`<path>.lock`) serializes the probe/remove/bind sequence. It is held for the listener's lifetime, so a second daemon on the same path fails fast.
- Closing the listener removes the socket on exit.

The reply protocol is one line of text, for interactive clients. Framing multi-line replies for programs (length-prefix or JSON Lines) is future work.

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
- A bearer token is required, loopback included, and every request must present it.
- TLS is a fronting proxy's job.

| Parameter | Required | Meaning |
|---|---|---|
| `type` | yes | `http` |
| `listen` | yes | Bind address `host:port` |
| `bearer_token_env` | yes | Environment variable holding the bearer token |
| `path` | no | Request path; defaults to `/chat` |

## Runtime

`serve`:

1. builds the model, the MCP tool clients, and one `conversation.Manager`;
2. runs the selected backends concurrently under an error group, each calling the manager's `submit`.

The runner wraps each backend's `submit` to prefix the conversation key with the backend name: `eng-slack:<channel-ts>`, `console:stdin`, `ops-sock:conn-7`.

- Conversations from different backends cannot collide in history or ordering.
- Logs and audit records name the originating backend.

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
| Unix socket | a short busy line |

**`Reply` is asynchronous.**

- The manager invokes it exactly once after the turn finishes, with the turn's text or a generic failure line, under a reply-delivery timeout.
- A failed `Reply` is logged, not retried, and does not fail the turn.
- Each backend keeps its delivery path alive until `Reply` runs. The HTTP handler blocks on the reply within the request; a socket connection stays open.
- Replies for different conversations may run at the same time. So a backend sharing one output destination synchronizes its writes: `stdio` guards stdout, and socket backends serialize per connection.

**Ordering.**

- Within one conversation, messages are processed and replied strictly in order.
- Independent conversations run concurrently, up to `max_concurrent_turns`.
- A concurrency slot is held only while the agent works, not while a reply is delivered.

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

A conversation key is never treated as identity.

- `User` is the authenticated principal the backend vouches for.
- Authorization keys on `User`, never on message text or the conversation key.

| Backend | `User` |
|---|---|
| `slack` | verified Slack user ID |
| `discord` | Discord user ID from the gateway event |
| `http` | the shared bearer token, one principal; per-user isolation is the upstream application's job until distinct identities exist |
| `stdio` | `local` today; Phase 4 uses the numeric user ID running the process |
| `unix` | none yet; Phase 4 uses the peer's numeric user ID |

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
- Each backend keeps waiting for an accepted message's reply through the drain window. It does not abandon the reply the moment its context is cancelled.

## Package layout

| Package | Holds |
|---|---|
| `internal/chat` | the `Backend` interface, the `New` factory, and the multi-backend runner |
| `internal/chat/<type>` | one backend each: `stdio`, `slack`, `discord`, `unix`, later `http` |
| `internal/chat/markdown` | Markdown table parsing shared by the platform backends |
| `cmd/chatops` | a single `serve` command |

Every source file has a one-to-one `_test.go`.

## Future work

- Teams, Webex, and SMS: webhook-based adapter backends. They reuse a shared HTTP listener and request-handling utilities, and are not configuration variants of the generic `http` backend.
- Per-user authorization (Phase 4), applied uniformly across backends, keyed on each backend's verified sender identity.
- A WebSocket backend, a candidate for interactive remote clients. It can send a bearer token in the handshake, but a shared token is weaker than a platform's own sign-in and does not tell users apart unless each user gets their own token.

How other platforms could take approval answers ([`policy.md`](policy.md#answers)). Text answers with request IDs work everywhere; buttons need platform support:

| Platform | Buttons | Click delivery |
|---|---|---|
| Telegram | inline keyboard | long polling, no public endpoint |
| Webex | Adaptive Cards | webhook or WebSocket |
| Teams | Adaptive Cards | Bot Framework webhook, needs an inbound endpoint |
| Google Chat | card buttons | HTTP or Pub/Sub |
| Mattermost | message buttons, interactive dialogs | a URL chatops serves, reachable from the Mattermost server |
| Matrix, Zulip | none; emoji reactions are the usual substitute | sync / event queue |

Deferred:

- A request-ID/retrieval mode for `http`; the accepted design is request/response only.

## Decisions

1. **One interface, explicit wiring.**
    - Every backend is a message source behind the one `Backend` interface.
    - It is wired through a `type`-switched factory, with no `init()` or blank import.
    - A message's `Reply` closure is the entire platform abstraction.
2. **Named configuration, config-only selection.**
    - Backends are a named `chat:` map keyed by a `type` discriminator, with multiple instances per type.
    - Every entry runs, so the file alone says what is on.
    - Startup aborts on any invalid entry.
3. **One shared manager, isolated namespaces.** All backends feed one `serve`-owned conversation manager. Keys are prefixed with the backend name, so histories and ordering never collide.
4. **Asynchronous, ordered, graceful.**
    - `submit` never blocks.
    - `Reply` fires exactly once, and is concurrency-safe.
    - Per-conversation order is preserved.
    - Overload is handled centrally (`ErrBusy`, mapped per backend).
    - Shutdown drains within a bounded grace period.
5. **Sender identity is separate from conversation identity.** A key is never treated as identity. The `http` shared-token principal does not isolate an application's own users.
6. **Every backend authenticates.** Each does it its own way (see [Backend types](#backend-types)). A backend that cannot authenticate is not built, so `tcp` and `udp` are dropped. For `unix`, the socket file's permissions decide who may connect, and the peer's user ID says who connected.
7. **Adapter extensibility.**
    - New webhook platforms (Teams, Webex, SMS) are adapter backends reusing the shared HTTP listener.
    - A platform with an outbound connection, like Slack Socket Mode or the Discord Gateway, uses that instead, and needs no public endpoint.
