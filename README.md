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
  tools      List available tools.
  version    Show build version.
```

## Running the system package

The Debian and RPM packages install a `chatops.service` systemd unit and a dedicated `chatops` system user. The service is not enabled or started automatically because the server requires deployment-specific chat and planner URLs.

Set the chat and planner URLs in `/etc/chatops/chatops.env`, adjust the other named settings as needed, then enable and start the service:

```ini
CHAT=slack://
PLANNER=ping://
CRED_STORE=json-file:///etc/chatops/credentials.json
CONNECTION_ID=operations
MAX_CONCURRENCY=8
LOG_LEVEL=info
LOG_FORMAT=json
EXTRA_ARGS=--tool ping --tool status-check --tool status-list
```

`EXTRA_ARGS` is expanded into separate command-line arguments and is intended for repeatable `--tool` selections or future options without a dedicated setting. Leave `CRED_STORE` or `EXTRA_ARGS` empty when they are not needed.

```bash
sudoedit /etc/chatops/chatops.env
sudo systemctl enable --now chatops.service
sudo systemctl status chatops.service
```

For a JSON credential store, place the file under `/etc/chatops`, make it readable by the service group, and set its URL in `CRED_STORE`:

```bash
sudo chown root:chatops /etc/chatops/credentials.json
sudo chmod 0640 /etc/chatops/credentials.json
```

Package upgrades preserve the environment file. Restart the service after changing its arguments or credentials with `sudo systemctl restart chatops.service`.

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
| `--credentials` | No | None | Credential store URL used by chat backends, planners, and tools. |
| `--connection-id` | No | `default` | Stable identifier used to scope planner conversation state. |
| `--max-concurrency` | No | `8` | Maximum conversations processed concurrently; the maximum value is `256`. |
| `--tool` | No | All selectable tools | Tool to expose to planners; repeat the flag to expose multiple tools. |
| `--log-level` | No | `info` | Log verbosity: `debug`, `info`, `warn`, or `error`. |
| `--log-format` | No | `json` | Log output format: `json` or `text`. |

A fully configured invocation looks like this:

```bash
chatops server \
    --chat telnet://chat.example.com:6023 \
    --planner ping:// \
    --credentials json-file:///etc/chatops/credentials.json \
    --connection-id operations \
    --max-concurrency 8 \
    --tool ping \
    --tool status-check \
    --tool status-list \
    --log-level info \
    --log-format json
```

The current server supports these URLs:

| Component | Scheme | URL form | Notes |
| --- | --- | --- | --- |
| Chat | `slack` | `slack://` | Uses Socket Mode with `slack.bot-token` and `slack.app-token` from the credential store; replies are threaded. |
| Chat | `telnet` | `telnet://host:port` | Port defaults to `23`; the protocol is newline-delimited text without telnet option negotiation. |
| Planner | `ping` | `ping://` | Recognizes ping requests and requires no credentials. |
| Planner | `openai-chat-completions` | `openai-chat-completions://host[:port][/path]?model=NAME` | Drives any OpenAI Chat Completions endpoint (OpenAI, Gemini, Ollama, …). See [OpenAI-compatible planner](#openai-compatible-planner). |
| Credentials | `json-file` | `json-file:///path/to/file.json` | Strict JSON document with optional `slack` and `planner` sections. A relative path or a leading `~` (e.g. `json-file://~/creds.json`) is accepted. |

With no `--tool` flag, the server exposes every compiled-in selectable tool, preserving the default behavior. Repeat `--tool` to expose an explicit allowlist; for example, `--tool ping --tool status-check` exposes exactly `ping://` and `status-check://`. An unknown name prevents startup and reports the available choices. A planner that attempts to use a compiled-in tool omitted from the allowlist receives the same unknown-tool error as any unavailable tool.

The server's internal `reply://` tool is bound directly to each live chat conversation and is therefore neither listed nor controlled by `--tool`. The first SIGINT or SIGTERM cancels in-flight work and closes resources gracefully; a second signal uses the operating system's default handling.

A failure while handling one message — the planner erroring, a plan naming an unknown tool, a tool rejecting its arguments or failing, even a tool panicking — is not fatal: the server logs the full error at `error`, posts a brief `sorry, I couldn't complete that request` back to that conversation, and keeps serving other messages. Only losing the chat connection or receiving a shutdown signal stops the server.

### Logging

The server emits structured logs (Go's `log/slog`) to standard error, describing how each message flows through the planner and the tools. `--log-level` sets the verbosity and `--log-format` selects `json` (default) or `text`.

At `info` the server logs startup configuration and, per message, `message received`, `plan produced` (with the step count and the tools the planner chose), each `executing step` (with the tool), `result posted`, and any error, all tagged with the `conversation_id` and `sender`. Raise to `--log-level debug` to also see the message text, the planner request, and each tool being opened and invoked. A representative `info` line:

```json
{"time":"2026-07-21T12:00:00Z","level":"INFO","msg":"plan produced","conversation_id":"C123","sender":"alice","steps":2,"tools":["reply://","status-check://"]}
```

Credentials are never logged: component URLs are logged, but secrets live in the credential store, not the URL.

### OpenAI-compatible planner

The `openai-chat-completions://` planner turns chat messages into plans using any service that speaks the OpenAI Chat Completions API. Because the endpoint is part of the URL, the same planner drives OpenAI, Google Gemini's OpenAI-compatible endpoint, a local Ollama, vLLM, LocalAI, and similar servers.

On each message the planner makes one completion request, offering the enabled operational tools (from `--tool`) plus a built-in `reply` function. The model's answer maps directly to plan steps: assistant prose and each `reply` call become `reply://` steps posted to the requester, and each operational tool call becomes a step invoking that tool. Every tool performs a single intent and describes itself (see [Adding a new tool](DEVELOPMENT.md)), so it is offered as its own typed function named for the tool, carrying its typed arguments with their required fields — so the model calls the tool with the arguments it actually needs instead of guessing. This mirrors the Model Context Protocol, where each tool has a flat input schema. Each function schema is a plain object (no `oneOf` or `const`), keeping it within the schema subset that OpenAI-compatible endpoints such as Gemini accept.

The URL configures the endpoint and model:

| Part | Meaning |
| --- | --- |
| host / port / path | Endpoint location. The host is required (this planner targets any compatible endpoint, not a fixed provider); the path defaults to `/v1`. |
| `model` (required) | Model identifier to request, for example `gpt-5`, `gemini-3.1-flash-lite`, or `llama3`. |
| `insecure=true` | Use plain HTTP instead of HTTPS, for a local server. |
| `keyless=true` | Explicitly omit authentication for a local or otherwise unauthenticated endpoint. |

By default the planner requires `planner.api-key` from the credential store and sends it as a bearer token. A missing or empty key prevents startup. Set `keyless=true` explicitly when the endpoint requires no authentication.

```bash
# OpenAI
chatops server --chat telnet://localhost:6023 \
    --planner 'openai-chat-completions://api.openai.com/v1?model=gpt-5' \
    --credentials json-file:///etc/chatops/credentials.json

# Google Gemini's OpenAI-compatible endpoint
chatops server --chat telnet://localhost:6023 \
    --planner 'openai-chat-completions://generativelanguage.googleapis.com/v1beta/openai?model=gemini-3.1-flash-lite' \
    --credentials json-file:///etc/chatops/credentials.json

# Local Ollama (no key required)
chatops server --chat telnet://localhost:6023 \
    --planner 'openai-chat-completions://localhost:11434/v1?insecure=true&keyless=true&model=llama3' \
    --tool ping --tool status-check --tool status-list
```

The exchange is single-shot: tool results are not fed back to the model, and the planner keeps no per-conversation history yet.

### Service status tool

The status tools let a planner check public service-status APIs without credentials. The `status-check://` tool takes a `service` argument, and the `status-list://` tool discovers the canonical services:

```go
planner.Step{
    Tool: "status-check://",
    Call: tool.Call{Arguments: map[string]string{"service": "github"}},
}
```

| Service | Provider | Aliases |
| --- | --- | --- |
| `github` | GitHub | `gh` |
| `anthropic` | Anthropic and Claude | `claude` |
| `cloudflare` | Cloudflare | `cf` |
| `openai` | OpenAI | — |
| `gemini` | Google Gemini across the Workspace Gemini and Vertex Gemini public incident feeds | `google`, `google-gemini`, `gemini-api`, `vertex-gemini`, `gemini-workspace` |
| `slack` | Slack | — |
| `docker-hub` | Docker Hub | `docker`, `dockerhub` |
| `all` | Every canonical service above | — |

The result text is ready for the engine to relay directly to chat, and `Result.Details` maps each checked canonical provider to its normalized health: `operational`, `maintenance`, `degraded`, `partial_outage`, `major_outage`, or `unknown`. For example:

```text
[OK] GitHub — All Systems Operational
[DEGRADED] OpenAI — Degraded Performance
  Elevated API errors (monitoring)
  https://status.openai.com/...
```

Provider requests for `all` are made concurrently with at most four in flight. A provider timeout, HTTP failure, or malformed response produces an `unknown` result instead of failing the tool step and stopping the engine; a missing or unknown `service` remains an error.

### Kubernetes resource tools

Two tools read Kubernetes resources for chat. `k8s-list://` lists resources of one type in a namespace or across all namespaces, and `k8s-get://` fetches specific resources by name as a describe-style brief, JSON, or YAML. Both resolve the resource type through the API server's discovery data and read objects with the dynamic client, so they serve built-in resources and CustomResourceDefinitions alike. The type may be given as a plural, singular, short name, or kind: `pods`, `po`, `pod`, `deployment`, `StatefulSet`, `deployments.apps`, or any installed CRD name.

#### Connecting to a cluster

Credentials never appear in a tool URL. How to reach a cluster — the API server address, the CA bundle that verifies its certificate, and the client certificate or token that authenticates to it — all come from a kubeconfig, exactly as `kubectl` reads one. The kubeconfig is located through the standard rules: the `KUBECONFIG` environment variable if set, otherwise `~/.kube/config`. When neither is present and the server runs inside a pod, it falls back to the pod's in-cluster service account (its token and the cluster CA are mounted automatically). Configuring `KUBECONFIG` once therefore serves every present and future Kubernetes tool; the tool URL only names which cluster and defaults to apply:

```text
k8s-get://                             current context, or in-cluster when running in a pod
k8s-get://?context=prod                a named kubeconfig context
k8s-get://?context=prod&namespace=web  a default namespace for calls that omit one
k8s-get://?kubeconfig=/path/to/config  an explicit kubeconfig file, overriding KUBECONFIG
```

Because the kubeconfig carries the server URL, the CA, and the client credentials together, there is nothing else to configure per tool. To point the server at a cluster reachable with a CA and a client certificate, embed them in a kubeconfig context — for example generated with `kubectl config set-cluster`, `set-credentials`, and `set-context` — then set `KUBECONFIG` to that file in the service environment (see [Running the system package](#running-the-system-package)). A bearer token or an exec credential plugin works the same way; the tools honor whatever the kubeconfig context specifies.

The systemd unit reads its environment from the package's configuration file, so add the line there to share one cluster across the Kubernetes tools:

```ini
KUBECONFIG=/etc/chatops/kubeconfig
```

#### Listing and getting

`k8s-list://` reads these arguments: `kind` (required), `namespace` (optional; defaults to the configured namespace and is ignored for cluster-scoped types), and `all-namespaces` (optional boolean, listing across every namespace). The result is an aligned table of name and age, prefixed with the namespace across namespaces and suffixed with a status column when the type reports one:

```go
planner.Step{
    Tool: "k8s-list://?context=prod",
    Call: tool.Call{Arguments: map[string]string{"kind": "pods", "namespace": "web"}},
}
```

`k8s-get://` reads `kind` (required), `name` (required), `namespace` (optional, as above), and `output` (optional: `brief`, `json`, or `yaml`). The default `brief` output is a describe-style summary — identity, age, labels, a status hint, and the resource's recent events — so a separate describe verb is unnecessary. `json` and `yaml` emit the full manifest. Pass several names as a comma-separated list to fetch them together in one call:

```go
planner.Step{
    Tool: "k8s-get://?context=prod",
    Call: tool.Call{Arguments: map[string]string{"kind": "statefulset", "name": "api,worker", "namespace": "web", "output": "yaml"}},
}
```

Secret values are always masked before rendering, in every output format: the `brief` summary omits a Secret's data entirely, and `json` and `yaml` keep the keys and shape but replace each value. The `kubectl.kubernetes.io/last-applied-configuration` annotation, which can carry a serialized copy of the original values, is stripped from Secrets as well, so a `Secret` never carries secret material into chat:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: web
data:
  password: '**REDACTED**'
  username: '**REDACTED**'
```

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

Both paths finish with the same two manual steps, because Slack does not expose these credentials through the manifest API: install the app to your workspace to obtain the bot token (`xoxb-…`), and generate an app-level token with the `connections:write` scope for the Socket Mode token (`xapp-…`). Store them as `slack.bot-token` and `slack.app-token`, respectively. The script prints the exact links for both. To serve channels beyond direct messages, add the matching `message.*` events and history scopes to the manifest before creating the app, and invite the bot to each channel.

Add both tokens to the credential store and start the server with the configuration-free `slack://` URL:

```json
{
  "slack": {
    "bot-token": "xoxb-...",
    "app-token": "xapp-..."
  }
}
```

```bash
chatops server --chat slack:// --planner ping:// --credentials json-file:///etc/chatops/credentials.json
```

Every typed command must start by mentioning the bot, including typed follow-up answers such as `@chatops yes`. Slack represents that recipient as a stable user-ID mention in the event payload; the backend requires that the leading mention exactly match the authenticated bot ID and strips it before passing `yes` to the planner. Mentions of other users do not authorize commands, and an `app_mention` with the bot mention later in its text is ignored. Renaming the bot from `@chatops` to `@bot` requires no server configuration change.

Confirmation questions expose Yes and No buttons in Slack. A button click is acknowledged through Socket Mode and delivered to the planner as the corresponding plain-text answer, so it does not need another bot mention. Only controls and values attached to a prompt posted by this process are accepted; a prompt can be answered once, expires after ten minutes, and is held in a cache of at most 4,096 entries. Invalid, expired, foreign, and duplicate interactions are ignored. After a valid selection, the buttons are removed. Backends without interactive controls, including telnet, keep the `(yes/no)` text and accept a typed answer.

Each root message starts a ChatOps conversation. The bot posts its response in that message's Slack thread, and mentioned human replies in the same thread retain planner state. Native channel/thread routes expire after 24 hours without inbound or outbound activity, and at most 4,096 routes are retained; when the cache is full, the earliest-expiring route is evicted. An indexed expiry heap keeps route refresh and eviction logarithmic instead of scanning every cached route. Unmentioned messages, bot messages, message subtypes such as edits and deletes, and empty messages are ignored to prevent accidental commands, reply loops, and processing of Slack system events.

### Credential file

The `json-file` credential store uses a predefined schema:

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

A complete sample with dummy values is available at [`scripts/cred-store-sample.json`](scripts/cred-store-sample.json). Both sections and every credential within them are optional, allowing configurations such as telnet plus the ping planner. Unknown sections, unknown fields, nulls, and non-string credential values are rejected when the store opens, so spelling and shape mistakes fail at startup. Credential values do not belong in chat backend, planner, or tool URLs. Credentials needed to open the store itself use that store backend's standard configuration chain.

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
openai
ping

$ chatops planners --json
["openai","ping"]
```

### Tools

List the selectable operational tools the binary knows about, one scheme per line. These are the names accepted by `server --tool`. Add `--json` (`-j`) for a machine-readable array:

```console
$ chatops tools
k8s-get
k8s-list
ping
status-check
status-list

$ chatops tools --json
["k8s-get","k8s-list","ping","status-check","status-list"]
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
