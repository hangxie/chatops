# chatops

`chatops` connects a chat backend to a planner and operational tools, then processes messages until interrupted.

## Quick start

This local test uses the built-in telnet chat backend and ping planner. It requires Go and netcat (`nc`) and does not require a credentials file.

From the repository root, start a TCP listener in the first terminal:

```bash
nc -l 6023
```

In a second terminal, start the server:

```bash
go run . server \
    --chat telnet://localhost:6023 \
    --planner ping://
```

Return to the netcat terminal, type `ping`, and press Enter:

```text
ping
pong
```

You can also exercise the planner's confirmation flow:

```text
can you ping the server?
do you want me to ping? (yes/no)
yes
pong
```

Press Ctrl+C in the server terminal to stop gracefully. Closing netcat instead disconnects the chat backend and causes the server to exit with a connection-loss error.

## Usage

```console
$ chatops --help
Usage: chatops <command>

Commands:
  chats      List available chat backends.
  planners   List available planner backends.
  server     Run the ChatOps server.
  version    Show build version.
```

### Server

The server requires one chat backend URL and one planner URL:

```bash
chatops server --chat telnet://localhost:6023 --planner ping://
```

Available settings:

| Flag | Required | Default | Description |
| --- | --- | --- | --- |
| `--chat` | Yes | — | Chat backend URL, such as `slack://` or `telnet://localhost:6023`. |
| `--planner` | Yes | — | Planner backend URL, such as `ping://`. |
| `--credentials` | No | None | Credential store URL used by planners and tools. |
| `--connection-id` | No | `default` | Stable identifier used to scope planner conversation state. |
| `--max-concurrency` | No | `8` | Maximum conversations processed concurrently; the maximum value is `256`. |

A fully configured invocation looks like this:

```bash
chatops server \
    --chat telnet://chat.example.com:6023 \
    --planner ping:// \
    --credentials json-file:///etc/chatops/credentials.json \
    --connection-id operations \
    --max-concurrency 8
```

The current server supports these URLs:

| Component | Scheme | URL form | Notes |
| --- | --- | --- | --- |
| Chat | `slack` | `slack://` | Uses Socket Mode with `SLACK_BOT_TOKEN` and `SLACK_APP_TOKEN`; replies are threaded. |
| Chat | `telnet` | `telnet://host:port` | Port defaults to `23`; the protocol is newline-delimited text without telnet option negotiation. |
| Planner | `ping` | `ping://` | Recognizes ping requests and requires no credentials. |
| Credentials | `json-file` | `json-file:///path/to/file.json` | Optional JSON object mapping credential names to string values. |

The server also wires the `ping://` and `status://` operational tools and its internal `reply://` tool. The first SIGINT or SIGTERM cancels in-flight work and closes resources gracefully; a second signal uses the operating system's default handling.

### Service status tool

The `status://` tool lets a planner check public service-status APIs without credentials. A planner invokes it with a `check` action and a provider target, or uses the `list` action to discover canonical targets:

```go
planner.Step{
    Tool: "status://",
    Call: tool.Call{Action: "check", Target: "github"},
}
```

| Target | Service | Aliases |
| --- | --- | --- |
| `github` | GitHub | `gh` |
| `anthropic` | Anthropic and Claude | `claude` |
| `cloudflare` | Cloudflare | `cf` |
| `openai` | OpenAI | — |
| `gemini` | Google Gemini across the Workspace Gemini and Vertex Gemini public incident feeds | `google`, `google-gemini`, `gemini-api`, `vertex-gemini`, `gemini-workspace` |
| `slack` | Slack | — |
| `docker-hub` | Docker Hub | `docker`, `dockerhub` |
| `all` | Every canonical target above | — |

The result text is ready for the engine to relay directly to chat, and `Result.Details` maps each checked canonical provider to its normalized health: `operational`, `maintenance`, `degraded`, `partial_outage`, `major_outage`, or `unknown`. For example:

```text
[OK] GitHub — All Systems Operational
[DEGRADED] OpenAI — Degraded Performance
  Elevated API errors (monitoring)
  https://status.openai.com/...
```

Provider requests for `all` are made concurrently with at most four in flight. A provider timeout, HTTP failure, or malformed response produces an `unknown` result instead of failing the tool step and stopping the engine; invalid actions, targets, or parameters remain errors.

### Slack

The Slack backend uses Socket Mode, so the server does not need a public HTTP endpoint. Create a Slack app with Socket Mode and Interactivity enabled, generate an app-level token with the `connections:write` scope, and install the app to obtain its bot OAuth token. Give the bot the `chat:write` scope plus the read scopes required by the events you subscribe to. At startup, the backend calls `auth.test` with the bot token to validate authentication and obtain the exact bot user ID; this method requires no additional bot scope.

Subscribe to the `app_mention` bot event and add `app_mentions:read`. To receive mentioned commands from direct messages too, subscribe to `message.im` and add `im:history`. The backend can also consume `message.channels`, `message.groups`, or `message.mpim` when their corresponding history scopes are granted, but unmentioned messages are ignored. Invite the bot to each channel it should serve.

#### Creating the app

The app configuration above is captured in [`scripts/slack-app-manifest.json`](scripts/slack-app-manifest.json), an [app manifest](https://api.slack.com/reference/manifests) with Socket Mode, Interactivity, the `app_mention` and `message.im` events, and the `app_mentions:read`, `chat:write`, and `im:history` bot scopes. Creating the app from this manifest is faster and less error-prone than setting each option by hand.

The recommended path needs no tooling or tokens, just a browser signed in to Slack with permission to add apps to the target workspace: open [Your Apps](https://api.slack.com/apps) — Slack's developer console, not the chat client — choose "Create New App" then "From an app manifest", pick the workspace, and paste the file contents. Workspaces that restrict app installation may require an admin to perform or approve this.

For repeat or automated provisioning, [`scripts/create-slack-app.sh`](scripts/create-slack-app.sh) posts the same manifest to Slack's App Manifest API instead. It needs `curl`, `jq`, and a configuration access token. Because that token is itself created in the browser, this path is only worthwhile when you provision apps more than once and can reuse the token's refresh token.

Create the configuration access token as follows:

1. Open [App Config Tokens](https://api.slack.com/reference/manifests#config-tokens) (or the "Your app configuration tokens" section at the bottom of [Your Apps](https://api.slack.com/apps)).
2. Click "Generate Token", select the workspace, and confirm.
3. Copy the generated access token (`xoxe.xoxp-…`, valid 12 hours) and, if you plan to rotate it, the refresh token (`xoxe-1-…`).

Refresh an expired access token without the browser by exchanging the refresh token through the [`tooling.tokens.rotate`](https://api.slack.com/methods/tooling.tokens.rotate) method. Then run the script:

```bash
export SLACK_CONFIG_ACCESS_TOKEN=xoxe.xoxp-...
./scripts/create-slack-app.sh
```

The app is named `chatops` by default. Pass a different name as an argument (or set `SLACK_APP_NAME`) to override both the app display name and the bot user name; the manifest itself is left unchanged:

```bash
./scripts/create-slack-app.sh opsbot
```

When pasting the manifest by hand instead, edit the `name` fields in [`scripts/slack-app-manifest.json`](scripts/slack-app-manifest.json) before pasting.

Both paths finish with the same two manual steps, because Slack does not expose these credentials through the manifest API: install the app to your workspace to obtain the bot token (`SLACK_BOT_TOKEN`, `xoxb-…`), and generate an app-level token with the `connections:write` scope for the Socket Mode token (`SLACK_APP_TOKEN`, `xapp-…`). The script prints the exact links for both. To serve channels beyond direct messages, add the matching `message.*` events and history scopes to the manifest before creating the app, and invite the bot to each channel.

Set both tokens and start the server with the configuration-free `slack://` URL:

```bash
export SLACK_BOT_TOKEN=xoxb-...
export SLACK_APP_TOKEN=xapp-...

chatops server --chat slack:// --planner ping://
```

Every typed command must start by mentioning the bot, including typed follow-up answers such as `@chatops yes`. Slack represents that recipient as a stable user-ID mention in the event payload; the backend requires that the leading mention exactly match the authenticated bot ID and strips it before passing `yes` to the planner. Mentions of other users do not authorize commands, and an `app_mention` with the bot mention later in its text is ignored. Renaming the bot from `@chatops` to `@bot` requires no server configuration change.

Confirmation questions expose Yes and No buttons in Slack. A button click is acknowledged through Socket Mode and delivered to the planner as the corresponding plain-text answer, so it does not need another bot mention. Only controls and values attached to a prompt posted by this process are accepted; a prompt can be answered once, expires after ten minutes, and is held in a cache of at most 4,096 entries. Invalid, expired, foreign, and duplicate interactions are ignored. After a valid selection, the buttons are removed. Backends without interactive controls, including telnet, keep the `(yes/no)` text and accept a typed answer.

Each root message starts a ChatOps conversation. The bot posts its response in that message's Slack thread, and mentioned human replies in the same thread retain planner state. Native channel/thread routes expire after 24 hours without inbound or outbound activity, and at most 4,096 routes are retained; when the cache is full, the earliest-expiring route is evicted. An indexed expiry heap keeps route refresh and eviction logarithmic instead of scanning every cached route. Unmentioned messages, bot messages, message subtypes such as edits and deletes, and empty messages are ignored to prevent accidental commands, reply loops, and processing of Slack system events.

### Credential file

The `json-file` credential store expects a single JSON object whose values are strings:

```json
{
  "db-password": "example-password",
  "api-token": "example-token"
}
```

Credential values do not belong in backend, planner, or tool URLs. Backends that require authentication resolve it from the credential store or their standard environment variables.

### Chats

List the chat backends the binary knows about, one scheme per line. These are the schemes accepted by `server --chat`. Add `--json` (`-j`) for a machine-readable array:

```console
$ chatops chats
slack
telnet

$ chatops chats --json
["slack","telnet"]
```

### Planners

List the planner backends the binary knows about, one scheme per line. These are the schemes accepted by `server --planner`. Add `--json` (`-j`) for a machine-readable array:

```console
$ chatops planners
ping

$ chatops planners --json
["ping"]
```

### Version

Print the short version or detailed build metadata:

```console
$ chatops version
v0.1.0

$ chatops version --all --json
{"Version":"v0.1.0","BuildTime":"2026-07-15T22:04:35-0700","Source":"github"}
```

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for package architecture, Go API examples, and instructions for adding credential stores, chat backends, tools, and planners.
