# chatops

[![](https://hangxie.github.io/chatops/coverage.svg)](https://hangxie.github.io/chatops/coverage-history.html)

`chatops` connects a chat platform to an OpenAI-compatible language model and to tools served over the [Model Context Protocol](https://modelcontextprotocol.io/specification) (MCP). The model chooses which tools to call, ChatOps enforces authorization and approval, and the answer is posted back to chat.

This project is under early development:

- `chatops serve` answers messages from every chat backend in its config: Slack, Discord, a local terminal, or a Unix socket.
- `chatops-mcp` serves the built-in tool groups: `ping`, `status`, read-only `kubernetes`, and `host` (see [Built-in MCPs](#built-in-mcps)).
- Authorization policy and approvals are not implemented yet.

## Binaries

| Binary | Purpose |
|---|---|
| `chatops` | ChatOps daemon, an MCP client that connects chat, model, and MCP servers |
| `chatops-mcp` | Built-in MCP server providing first-party tools, usable by any MCP client |

## Build

Building requires Go and `make`:

```bash
make build
./build/chatops version
./build/chatops-mcp version
```

`make all` runs dependency, format, lint, test, and build steps.

### Coverage history

`make pages-coverage` (or `make pages`) generates the project's GitHub Pages content to `build/pages/`:

- It checks out each day's latest commit, runs `go test`, and appends results to `build/coverage.csv`, sorted chronologically.
- Days with no commits carry forward the previous day's coverage.
- Days before the first commit with non-zero coverage are skipped.
- It writes the per-package HTML coverage report to `build/pages/coverage.html` and the README badge to `build/pages/coverage.svg`.

```bash
make pages-coverage                                                     # last 7 completed UTC days (default)
make pages-coverage COLLECT_ARGS="--start 2026-10-01"                   # full history from a date
make pages-coverage COLLECT_ARGS="--start 2026-10-01 --end 2026-10-10"  # explicit range
```

The HTML chart and badge need no third-party Python modules; the companion PNG requires `matplotlib` and is skipped with a warning when it is missing.

The `github-pages` workflow runs the same target weekly and publishes the result to <https://hangxie.github.io/chatops/>, which also serves the badge above. It seeds `build/coverage.csv` from the previously published copy, so history accumulates across runs rather than living in the repository.

## Configuration

`chatops` reads a single YAML file. The rules:

- It holds exactly one YAML document. A second one (after `---`) is an error, not silently ignored.
- Unknown fields are rejected, and every invalid field is reported at startup.
- Secrets never appear in the file. Settings ending in `_env` name the environment variable that holds the secret; an unset or empty variable is an error.
- A bearer token or API key is only sent over `https`, or plain `http` to a loopback host (`localhost`, `127.0.0.0/8`, `::1`). Any other `http` URL combined with `api_key_env` or `bearer_token_env` is rejected.

Start from the annotated sample at [`package/systemd/config.yaml`](package/systemd/config.yaml). It carries every setting with inline comments and is verified by the test suite. The table below is the authoritative explanation of each setting.

| Setting | Default | Description |
|---|---|---|
| `chat.<name>.type` | (required) | Chat backend kind: `slack`, `discord`, `stdio`, or `unix`; `<name>` is a free-form id; every backend listed runs, and at least one is required |
| `chat.<name>.bot_token_env`, `app_token_env` | (none) | slack: environment variables holding the Slack bot (`xoxb-`) and app-level (`xapp-`) tokens; both required |
| `chat.<name>.bot_token_env` | (none) | discord: environment variable holding the Discord bot token; required |
| `chat.<name>.reply_mode` | slack `thread`, discord `channel` | slack and discord: where a top-level message is answered, `thread` or `channel`; a message in an existing thread is always answered in that thread |
| `chat.<name>.path`, `mode` | (none) | unix only: filesystem path of the socket to create (required) and optional octal permission bits (e.g. `0600`, the default owner-only) |
| `llm.base_url` | (required) | OpenAI-compatible API base; requests go to `<base_url>/chat/completions` |
| `llm.model` | (required) | Model name sent with each request |
| `llm.api_key_env` | (none) | Environment variable holding the API key, sent as a bearer token; requires an `https` `base_url` unless the host is loopback |
| `llm.disable_thinking` | `false` | Send `reasoning_effort: "none"` and `chat_template_kwargs: {"enable_thinking": false}`; the packaged sample turns it on; leave off for endpoints that reject unknown fields |
| `llm.sampling.*` | (unset) | Optional generation parameters; see [Sampling](#sampling) |
| `mcp.servers.<id>.transport` | (required) | `stdio` or `streamable-http` |
| `mcp.servers.<id>.command`, `args` | | stdio only: the server process to launch |
| `mcp.servers.<id>.env` | (none) | stdio only: extra environment variables; values are stored in the file, so never put secrets here |
| `mcp.servers.<id>.secret_env` | (none) | stdio only: maps a variable for the server to the `chatops` environment variable holding its value, for secrets such as tokens; a name may not appear in both `env` and `secret_env` |
| `mcp.servers.<id>.url`, `bearer_token_env` | | streamable-http only: endpoint and optional bearer token variable; a token requires an `https` URL unless the host is loopback |
| `agent.system_prompt` | (required) | Instructions prepended to every turn; edit it to tune behavior without rebuilding, then restart the daemon. Startup fails if it is empty. The packaged sample ends with `/no_think` as a Qwen3 prompt-level backup to `llm.disable_thinking`; drop it for other models |
| `agent.max_iterations` | `20` | Maximum model calls per turn |
| `agent.turn_timeout` | `120s` | Wall-clock limit for one turn; a turn that runs past it fails even if a late model or tool reply arrives |
| `agent.tool_timeout` | `30s` | Limit for one tool call; a timed-out call is reported to the model, which may continue; must not exceed `turn_timeout` |
| `agent.max_tool_result_bytes` | `65536` | Tool output beyond this is truncated before it reaches the model; a short truncation marker is appended on top of the limit |
| `agent.history_turns` | `0` | Earlier user messages replayed as context so a follow-up such as "check it again" can resolve. `0` (the default) sends no history, so every request is stateless. See [Conversation history](#conversation-history) for the trade-off |
| `agent.history_ttl` | `24h` | When `history_turns` is positive, a conversation idle this long (since its last answered message, failed or not) starts over with no history |
| `agent.max_concurrent_turns` | `4` | Agent turns (model and tool calls) running at once across all conversations; posting a reply does not count; turns within one conversation always run one at a time, each after the previous reply is posted |
| `agent.max_pending_messages` | `64` | Accepted but unfinished messages across all conversations; more are refused as busy |
| `agent.show_stats` | `false` | Append each turn's statistics to its reply, failed turns included: every model and tool step with duration, tokens, and result size, then the totals. See [`docs/observability.md`](docs/observability.md) |
| `agent.require_tool_call` | `true` | Force a tool call on each turn's first model call (`tool_choice: required`), so the model cannot answer from memory; off-topic messages go to the built-in `decline` tool. A first call that still returns text fails the turn. Set `false` for endpoints that reject `required`, such as vLLM without `--tool-call-parser` |
| `agent.decline_reply` | `I can only help with operations questions about the systems I monitor.` | Reply when the model declines a message, whether by calling `decline` or by writing this exact text. Must not be empty |
| `metrics.listen` | (none) | `host:port`, such as `127.0.0.1:9464`, serving Prometheus metrics at `/metrics` and probes at `/healthz` and `/readyz`; unset disables it. See [Metrics](#metrics) and [Health checks](#health-checks) |

About MCP servers:

- Each model-facing tool is named `<server>__<tool>`, for example `kubernetes__pod_list`. The sample config runs each built-in group as its own server, named after the group.
- A stdio server inherits only `PATH` and `HOME` from `chatops`, plus its configured `env` and `secret_env`. Chat and model credentials never reach tool processes unless a `secret_env` entry names them.
- All servers must be reachable at startup.

### Sampling

Each `llm.sampling` parameter is sent only when set, so an unset one keeps the endpoint default.

| Parameter | Range | Note |
|---|---|---|
| `temperature` | 0–2 | `0` is greedy and deterministic, the most reliable choice for an ops assistant |
| `top_p` | 0–1 | |
| `max_tokens` | >0 | |
| `frequency_penalty`, `presence_penalty` | −2–2 | |
| `seed` | | |
| `stop` | list of strings | |
| `top_k` | ≥0 | vLLM/llama.cpp extension; strict OpenAI endpoints reject it |
| `min_p` | 0–1 | vLLM/llama.cpp extension |
| `repetition_penalty` | >0 | vLLM/llama.cpp extension |

### Conversation history

`agent.history_turns` sets how many of a conversation's earlier user messages are replayed as context on each new message.

- Only user messages are replayed, never assistant answers, tool calls, or tool results, so an earlier reply can never bias a later one.
- It trades conversational feel against per-answer focus, and the right value depends on your model.
- It is about isolation, not factual accuracy. Correctness still comes down to the tools, the model, and the prompt; history only controls how much earlier conversation the model can see, and drag into the reply.
- History older than `agent.history_ttl` is dropped, so an idle conversation starts fresh.

| Value | Follow-ups ("check it again") | Risk | Use for |
|---|---|---|---|
| `0` (default) | Have no referent; misread or refused | None: every message is answered in isolation | One-shot status questions |
| `1` | Short ones resolve ("and Claude Code?") | Small models (1.5–2B) occasionally restate the previous subject | The best start if you want follow-ups |
| `3`, `5`, … | References reach several turns back | Each past question has no "already answered" marker, so small models pile unrelated systems into replies and drift | Larger, instruction-following models; raise gradually and watch for answers about things you did not ask |

Whatever the value, a strong `system_prompt` helps. The packaged prompt makes the model:

1. decide first whether a message is an operational request its tools can cover;
2. decline anything else, greetings and small talk included, with a fixed sentence rather than engaging;
3. otherwise answer only the latest request, resolving implicit references from history.

## Chat backends

`chatops serve` runs the daemon against every chat backend in its config. See [`docs/chat.md`](docs/chat.md) for the design.

- Backends are declared in the `chat:` map, each with a free-form name and a `type`.
- Every entry runs. To turn one off, comment it out or remove it; no flag or environment variable selects backends.
- Several can run at once, all sharing one agent and conversation manager.

```yaml
chat:
  eng-slack:
    type: slack
    bot_token_env: SLACK_BOT_TOKEN
    app_token_env: SLACK_APP_TOKEN
  console:
    type: stdio
```

| Flag | Default | Description |
|---|---|---|
| `-c`, `--config` | (required) | Path to the YAML config file |
| `--log-level` | `info` | Minimum level of logs written to stderr: `debug`, `info`, `warn`, or `error`; `debug` adds each tool call's arguments; see [Debug logs](#debug-logs) |
| `--log-format` | `text` | Log format on stderr: `text` or `json` |

Startup aborts if `chat:` is empty or any backend is invalid.

Stopping the daemon (SIGINT or SIGTERM) stops accepting new messages and gives in-flight turns and their replies a short grace period to finish. Anything still running past it is cancelled, and conversation history is lost.

A tool call that fails is reported by chatops itself, whatever the model wrote, so an expired credential or an unreachable system is never hidden behind a vague answer:

```text
I couldn't list the namespaces.

Tool errors:
- `kubernetes.namespace_list`: Unauthorized
```

- One line per failing call: the error's first line, cut to 200 characters. Error text can name internal hosts.
- A failure is cleared only when the same tool with the same arguments later succeeds in the turn; a success on another namespace or target leaves it listed.
- Server errors, lost connections, and timeouts are listed. Unknown tools and invalid arguments are the model's own mistakes, fed back to it, and are not listed.

Declines and outages are kept apart:

| Situation | Reply |
|---|---|
| The model declines | `agent.decline_reply`, plus `Unavailable tool servers: ...` when any MCP server is down |
| The model declines while a tool failure is unresolved | only the tool errors, never the off-topic sentence |
| The model refuses in its own words, such as `Sorry, I can only help...` | the text as written, with the tool errors appended; only the `decline` call or the exact `decline_reply` text counts as a decline |
| No MCP tool is available at all | a "no tools are available" reply, without calling the model |

| Backend | Reaches users through | Public endpoint |
|---|---|---|
| [`stdio`](#stdio--interactive-terminal) | the terminal it runs in; development only | no |
| [`slack`](#slack--socket-mode) | Slack Socket Mode | no |
| [`discord`](#discord--gateway) | the Discord Gateway | no |
| [`unix`](#unix--local-socket) | a local Unix socket | no |

### `stdio` — interactive terminal

The `stdio` backend is an interactive development harness: one conversation on stdin/stdout.

- It needs a terminal and is not for an unattended daemon; under systemd it reads immediate EOF and exits.
- Type a message per line; `/quit`, `/exit`, or end of input stops it.
- A failed turn (model unreachable, turn timeout) prints `error: ...`, and the session continues.

```bash
make build
./build/chatops serve --config config.yaml   # with only a stdio backend under chat:
```

```text
> is the MCP server reachable?
The MCP server is reachable.
>
```

### `slack` — Socket Mode

The `slack` backend answers Slack messages over Socket Mode, so it needs no public endpoint.

1. At https://api.slack.com/apps, choose "Create New App", then "From an app manifest", and paste `scripts/slack-app-manifest.json`; edit the name first if you want a different bot name.
2. Install the app to the workspace and export its `xoxb-` bot token; generate an app-level `xapp-` token with the `connections:write` scope and export it too. The variable names are whatever the backend's `bot_token_env` and `app_token_env` say.
3. Start the daemon:

```bash
export SLACK_BOT_TOKEN=xoxb-... SLACK_APP_TOKEN=xapp-...
./build/chatops serve --config config.yaml
```

How it answers:

| Where | Answered when | Reply goes to |
|---|---|---|
| Channel the app is invited to | the message starts with a mention of the bot (`@chatops is the MCP server reachable?`); a later mention is ignored, and thread follow-ups need it too | the message's thread, or the channel with `reply_mode: channel` |
| Direct message | always; no mention needed | the message's thread, or the DM with `reply_mode: channel` |
| Existing thread | as above | always that thread, regardless of `reply_mode` |

Conversations and ordering:

- Each thread is a separate conversation with its own history, so context never crosses threads. A top-level message starts a new conversation.
- Channel replies work poorly with `agent.history_turns > 0`: separate top-level messages do not share history, so `@chatops check again` cannot refer to the previous request. Use `history_turns: 0` with channel replies, or keep follow-ups in the original thread.
- Messages in one thread are answered one at a time, in order. Different threads are answered concurrently, up to `agent.max_concurrent_turns`.
- Slack retries and duplicate deliveries of an accepted message are answered once. A message refused as busy may be accepted if Slack delivers it again.

Busy, failures, and safety:

- When `agent.max_pending_messages` is reached, new messages get a short "try again shortly" reply. At most four such replies are posted at a time; further refused messages are only logged.
- A failed turn gets a short apology. The details, which may include internal endpoints, go only to the log.
- Busy and failure replies follow the same routing as answers.
- Replies are posted as plain text with link previews off, so model output cannot mention `@channel` or users, or make Slack fetch a URL.

> [!WARNING]
> Every message is answered with every configured tool available. Per-user permissions come in a later release, and `serve` logs a warning at startup until then. Until that release, configure only read-only tools, and only invite the app where all members may use all of its tools.

### `discord` — Gateway

The `discord` backend answers Discord messages over the Gateway, a websocket the bot opens outbound, so like `slack` it needs no public endpoint.

1. At https://discord.com/developers/applications, choose "New Application", then on the "Bot" page choose "Reset Token" and export the token. The bot needs no privileged gateway intents: without the Message Content intent, Discord still sends it the text of messages that mention it and of direct messages.
2. Invite the bot with `https://discord.com/oauth2/authorize?client_id=<application id>&scope=bot&permissions=309237713920`, which grants View Channels, Send Messages, Create Public Threads (needed for `reply_mode: thread`), Send Messages in Threads, and Read Message History (needed to reply to a message).
3. Add a backend such as `discord: {type: discord, bot_token_env: DISCORD_BOT_TOKEN}` and start the daemon:

```bash
export DISCORD_BOT_TOKEN=...
./build/chatops serve --config config.yaml
```

How it answers:

| Where | Answered when | Reply goes to | Conversation |
|---|---|---|---|
| Server channel | the message starts with a mention of the bot (`@chatops is the MCP server reachable?`) | a reply in the channel, or a new thread on the message with `reply_mode: thread` | each message is its own, joined by the thread it opens |
| Thread | the message starts with a mention of the bot | that thread | the whole thread shares history |
| Direct message | always | a reply in the DM | each message is its own |

- Messages from other bots and webhooks are ignored.
- With `reply_mode: thread`, the thread is named after the question and archives after a day of inactivity. Follow-ups in it still need a mention. If the bot cannot start a thread, it replies in the channel instead.
- Keep follow-ups in a thread when `agent.history_turns > 0`, either one started by `reply_mode: thread` or one started by hand.
- Replies ping no one, not even the asker, and have link previews off, so model output cannot mention `@everyone` or users, or make Discord fetch a URL.
- Markdown is shown as Discord renders it, except tables, which become aligned code blocks.
- A long answer is split at line breaks into up to five messages of 2,000 characters, ending with "[reply truncated]" if longer.
- The `agent.show_stats` footer is shown below the answer in smaller grey text.
- When `agent.max_pending_messages` is reached, new messages get a short "try again shortly" reply, as on Slack. It is always a channel reply, even with `reply_mode: thread`.

### `unix` — local socket

The `unix` backend lets a client on the same host reach a running daemon over a Unix domain socket. Unlike `stdio`, it is the right way to drive a daemon locally.

- One conversation per connection, a message per line.
- Access control is the socket file's permissions (`mode`, default owner-only), so it needs no token.
- A live socket refuses a second daemon's startup. An exclusive lock file (`<path>.lock`), held for the daemon's lifetime, keeps two instances from racing on the same path.

For example, with a backend named `ops` of `type: unix` and `path: /run/chatops/ops.sock`:

```bash
./build/chatops serve --config config.yaml   # every backend in the config, the socket included
nc -U /run/chatops/ops.sock                  # then talk to it, one message per line
```

### Debug logs

At `--log-level debug`, `serve` logs a `tool call requested` line with the arguments the model passed to each tool, up to 1 KiB, which shows which tool and inputs the model chose:

```text
level=DEBUG msg="tool call requested" tool=status__service_status arguments="{\"service\": \"openai\"}"
```

Turn it on only while troubleshooting.

| Appears in debug logs? | |
|---|---|
| Slack and Discord tokens, the LLM API key, MCP tokens, `secret_env` values | never; the processes read them directly, and they never reach the model |
| What users typed, such as a password pasted into a question | yes, if the model repeats it in tool arguments |
| Earlier tool results | yes, likewise |

- At `info` and above, `serve` logs chat user and conversation IDs, but never message text.
- Under systemd, logs go to the journal, usually readable by the `adm` and `systemd-journal` groups: a wider audience than `/etc/chatops/chatops.env`.

### Metrics

With `metrics.listen` set, `serve` exposes Prometheus metrics at `http://<listen>/metrics`. The full list is in [`docs/observability.md`](docs/observability.md).

| Area | Metrics |
|---|---|
| Questions, per chat backend | received, rejected, answered; response time from receipt to delivered reply |
| Model | call latency and tokens |
| Tools, per MCP server and tool | call latency, result size, truncation |
| Health | chat backend and MCP server up |
| Process | Go runtime and process metrics |

- The packaged `config.yaml` sets `metrics.listen: 127.0.0.1:9464`: on, but reachable only from the host. To scrape from another machine, bind it to an address that machine can reach.
- The endpoint has no authentication, so bind it to loopback or a network only your Prometheus can reach.
- If the endpoint cannot listen at startup, or stops unexpectedly later, `serve` exits with an error, so the service manager restarts it rather than running without metrics.

A scrape config:

```yaml
scrape_configs:
  - job_name: chatops
    static_configs:
      - targets: ["chatops.example.internal:9464"]
```

Average response time for answered questions over five minutes:

```promql
rate(chatops_response_duration_seconds_sum{outcome="answered"}[5m])
  / rate(chatops_response_duration_seconds_count{outcome="answered"}[5m])
```

### Health checks

The `metrics.listen` endpoint also serves two probes that answer in plain text, for a Kubernetes probe, a container health check, or an uptime monitor:

| Path | `200 ok` when | Otherwise |
|---|---|---|
| `/healthz` | the daemon is serving HTTP | no answer |
| `/readyz` | startup has finished, the daemon accepts questions, every chat backend can receive messages, and every MCP server is up | `503` listing each failure: `starting`, `shutting down`, `chat backend <name>: not connected`, or `mcp server <name>: down` |

- Use `/healthz` for liveness and `/readyz` for readiness or alerting.
- A Slack backend counts as connected once Socket Mode connects, a Discord backend once its gateway session is ready. Both are briefly not connected while they reconnect.
- A disconnected MCP server stays down until `serve` restarts, so a `/readyz` that keeps failing on one means a restart is needed.
- The probes check only cached state and never call the model or a tool, so a slow model endpoint does not fail them.

For example:

```sh
curl -fsS http://127.0.0.1:9464/readyz
```

### Run as a service

The deb and rpm packages install both binaries, a `chatops` system user, and a systemd unit that runs `chatops serve` as that user:

| Path | Purpose |
|---|---|
| `/etc/chatops/config.yaml` | Config file; set `llm.base_url` and `llm.model` before starting. It holds no secrets, so a new install makes it world-readable (`0644`) for troubleshooting. An upgrade never loosens a file installed `0640` by an earlier release: an edited one keeps its mode on both packages, and the deb keeps it on an unedited one too, so `chmod 0644` it yourself; never put a secret in an MCP server's `env:` |
| `/etc/chatops/mcp-status.d/` | Config directory of the `status` tool group; `00-default.yaml` is the shipped service list, and your own services go in files of your own beside it |
| `/etc/chatops/mcp-kubernetes.d/` | Config directory of the `kubernetes` tool group; `00-default.yaml` points at the kubeconfig at `/etc/chatops/kubeconfig` (which you provide; make it `root:chatops` `0640`) |
| `/etc/chatops/mcp-host.d/` | Config directory of the `host` tool group; `00-default.yaml` checks only `localhost`, so list your hosts in a file of your own beside it |
| `/etc/chatops/chatops.env` | Secrets (`SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`, `DISCORD_BOT_TOKEN`, and the LLM key named by `llm.api_key_env`), and `LOG_LEVEL`/`LOG_FORMAT`, which apply from the first log line; readable only by root and the `chatops` group (`0640`) |
| `/run/chatops/` | Socket directory of the `homelab-unix` backend, `0755` and owned by `chatops`; a tmpfiles.d entry (`/usr/lib/tmpfiles.d/chatops.conf`) recreates it at every boot |
| `chatops.service` | Runs `chatops serve --config /etc/chatops/config.yaml`, which starts every backend under `chat:`, with JSON logs to the journal; restarts on failure |

Edited config files are kept across upgrades; see [Configuration directories](#configuration-directories). The service is not started on install. The sample config runs `homelab-slack`, `homelab-discord`, and `homelab-unix`, and a backend whose token is empty fails startup, so set both the Slack and Discord tokens or comment out the backend you do not use. Once both files are filled in:

```bash
sudo systemctl enable --now chatops
journalctl -u chatops -f
```

## Built-in MCP server

`chatops-mcp serve` exposes tool groups to any MCP client. By default it speaks MCP over stdio, which is how an MCP client normally launches it as a subprocess.

| Flag | Default | Description |
|---|---|---|
| `--tool` | `ping` | Tool group to serve; each process serves one group |
| `--config` | (none) | Config directory of the group; every `*.yaml` in it is merged in file name order (see [Configuration directories](#configuration-directories)); required by `status` and `host`, optional for `kubernetes`, rejected by `ping` |
| `--http` | (stdio) | Serve Streamable HTTP at `/mcp` on this address instead of stdio |
| `--token-env` | (none) | Environment variable holding the bearer token HTTP clients must send; required when `--http` is not a loopback address |

### Built-in MCPs

Each tool group has a design doc under [`docs/`](docs/); the overall architecture is [`docs/DESIGN.md`](docs/DESIGN.md).

| Tool group | Tools | Purpose | Design |
|---|---|---|---|
| `ping` | `ping` | Connectivity smoke test; replies `pong` | [`docs/mcp-ping.md`](docs/mcp-ping.md) |
| `status` | `service_status` | Status and active incidents of external services from their status pages, and of self-hosted services from their health endpoints | [`docs/mcp-status.md`](docs/mcp-status.md) |
| `kubernetes` | 34 tools named `<resource>_<verb>`; see [Kubernetes tools](#kubernetes-tools) | Read-only Kubernetes inspection | [`docs/mcp-k8s.md`](docs/mcp-k8s.md) |
| `host` | `host_status` | Reachability of configured hosts from the ChatOps server: DNS resolution, ICMP ping, and the SSH identification | [`docs/mcp-host.md`](docs/mcp-host.md) |

### Kubernetes tools

| Resource | Tools | Shows |
|---|---|---|
| node | `node_list`, `node_status`, `node_resources` | roster, health, capacity and utilization |
| namespace | `namespace_list` | namespaces, and why one is stuck terminating |
| deployment | `deployment_list`, `deployment_pods`, `deployment_describe` | health, pods, definition |
| pod | `pod_list`, `pod_describe`, `pod_log`, `pod_resources` | roster, definition, logs, resource usage |
| statefulset | `statefulset_list`, `statefulset_pods`, `statefulset_describe` | health, pods, definition |
| daemonset | `daemonset_list`, `daemonset_pods`, `daemonset_describe` | health, pods, definition |
| replicaset | `replicaset_list`, `replicaset_pods`, `replicaset_describe` | health, pods, definition |
| service | `service_list`, `service_pods`, `service_describe` | health, backing pods, definition |
| job | `job_list`, `job_pods`, `job_log`, `job_describe` | status, pods, logs, definition |
| pv | `pv_list`, `pv_describe` | persistent volumes and their claims |
| pvc | `pvc_list`, `pvc_describe` | persistent volume claims |
| configmap | `configmap_list`, `configmap_describe` | configmaps |
| event | `event_list` | recent events and warnings |

Per-object tools take a list, so a question about several objects is one call: `node_list` for the names, then one `node_resources` call with all of them.

| Rule | Value |
|---|---|
| Argument | a list named after the resource (`nodes`, `pods`, `deployments`, ...); `{namespace, name}` objects for namespaced resources |
| Targets per call | at most 50 distinct; 5 for every `_describe` and `_log` tool, whose output is a full object or raw container output |
| Missing target | reported as not found, without failing the rest |
| Missing namespace | named as such |
| Rows per `_list` tool, and per target of a `_pods` tool | at most `max_results` (default 50), with the full count in `total` and a "showing the first N of M" note, so the model narrows the call instead of assuming it saw everything |

Settings live in the group's `--config` directory, shipped as `package/systemd/mcp-kubernetes.d/` and installed as `/etc/chatops/mcp-kubernetes.d/`. Every field is optional; without `--config` the group uses its defaults.

| Field | Meaning | Unset |
|---|---|---|
| `kubeconfig` | a kubeconfig honored exactly, with no fallback; a relative path follows the config directory | in-cluster credentials when running as a pod, else the default local kubeconfig |
| `context` | a kubeconfig context | the kubeconfig's current context |
| `max_results` | row cap for list and pods tools | 50 |

See [`docs/mcp-k8s.md`](docs/mcp-k8s.md) for the connection model. Node utilization needs metrics-server; without it, capacity and allocatable are still reported.

The group needs only read-only RBAC:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: chatops-reader
rules:
- apiGroups: [""]
  resources: [nodes, pods]
  verbs: [get, list]
- apiGroups: [""]
  resources: [namespaces]
  verbs: [get, list]
- apiGroups: ["apps"]
  resources: [deployments]
  verbs: [get, list]
- apiGroups: ["apps"]
  resources: [replicasets]
  verbs: [get, list]
- apiGroups: [""]
  resources: [pods/log]
  verbs: [get]
- apiGroups: ["apps"]
  resources: [statefulsets]
  verbs: [get, list]
- apiGroups: ["apps"]
  resources: [daemonsets]
  verbs: [get, list]
- apiGroups: ["apps"]
  resources: [controllerrevisions]
  verbs: [list]
- apiGroups: [""]
  resources: [services]
  verbs: [get, list]
- apiGroups: ["discovery.k8s.io"]
  resources: [endpointslices]
  verbs: [list]
- apiGroups: ["batch"]
  resources: [jobs]
  verbs: [get, list]
- apiGroups: [""]
  resources: [persistentvolumes]
  verbs: [get, list]
- apiGroups: [""]
  resources: [persistentvolumeclaims]
  verbs: [get, list]
- apiGroups: [""]
  resources: [configmaps]
  verbs: [get, list]
- apiGroups: [""]
  resources: [events]
  verbs: [list]
- apiGroups: ["metrics.k8s.io"]
  resources: [nodes, pods]
  verbs: [get, list]
```

> [!IMPORTANT]
> This role is read-only, but its reads are cluster-wide: pod metadata, workload pod templates, container logs, and configmap values in every namespace. The grant itself is the real boundary, so scope the binding or namespaces if that is broader than intended.

| Redacted before reaching chat? | |
|---|---|
| Literal environment values in `_describe` output | yes; values of 12 or more characters keep their last 3 |
| Secrets in container `command` or `args` | no |
| Anything a container writes to its logs | no |

`node_resources` sources:

| Figure | Source |
|---|---|
| CPU, memory, disk, and pod capacity and allocatable | node API |
| CPU and memory utilization | metrics API |
| Pod count | pod API |
| Disk utilization | kubelet Summary API, only with `get` on `nodes/proxy`; otherwise each node notes it is not permitted |

To enable disk utilization:

```yaml
- apiGroups: [""]
  resources: [nodes/proxy]
  verbs: [get]
```

- `nodes/proxy` is powerful: it reaches every kubelet endpoint, not just the Summary API. Grant it only when node disk reporting is wanted.
- The Summary API reports the kubelet's own filesystem, not other host disks; a separate data disk or Ceph OSD will not appear. Use Prometheus with node-exporter for full host disk monitoring.

### Status and host tools

The `status` group has no built-in services; every service comes from its `--config` directory. See [`docs/mcp-status.md`](docs/mcp-status.md) for the entry schema and page types.

- The shipped list is `package/systemd/mcp-status.d/00-default.yaml`, installed as `/etc/chatops/mcp-status.d/00-default.yaml`.
- Add your own services, such as self-hosted ones, in a file of your own beside it (for example `10-homelab.yaml`, same schema) rather than editing the shipped one. [Configuration directories](#configuration-directories) explains how files merge and how upgrades treat them.
- Restart the server after a change.

The `host` group checks only the hosts listed in its `--config` directory, never an address a user types. See [`docs/mcp-host.md`](docs/mcp-host.md) for the schema and health rule.

```text
resolve name ──▶ ping selected address ─┐
             └─▶ read SSH banner (no auth) ─┴─▶ UP / DEGRADED / UNREACHABLE / UNKNOWN
```

| Health | Meaning |
|---|---|
| `UP` | every check passed |
| `DEGRADED` | some checks passed, or the host answered but not as expected |
| `UNREACHABLE` | no probe answered from the ChatOps server; not proof the machine is off |
| `UNKNOWN` | the name did not resolve, or the checks could not run |

Host setup notes:

- Ping uses an unprivileged ICMP socket, so on Linux the `chatops` group must be inside `net.ipv4.ping_group_range`. Recent systemd sets it to every group; `sysctl net.ipv4.ping_group_range` shows it.
- sshd logs every SSH check as a connection closed before authentication, so allow-list the ChatOps host if fail2ban runs in `aggressive` mode.

### Trying it by hand

To exercise the server by hand over stdio, send an MCP `initialize` handshake followed by a tool call. The trailing `sleep` keeps stdin open until the replies are written:

```bash
(printf '%s\n' \
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"demo","version":"0"}}}' \
    '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
    '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{}}}'; sleep 1) \
    | ./build/chatops-mcp serve
```

The second reply carries the result:

```text
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"pong"}],"structuredContent":{"reply":"pong"}}}
```

Per-object Kubernetes tools take their targets as a list; with `--tool kubernetes`, this third line reports several nodes in one call:

```text
'{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"node_resources","arguments":{"nodes":["master-0","master-1"]}}}'
```

### Serving over HTTP

Run `./build/chatops-mcp serve --http 127.0.0.1:8080` and point an MCP client at `http://127.0.0.1:8080/mcp`. Press Ctrl+C to stop.

- Without `--token-env` the endpoint has no authentication, so `serve` refuses any non-loopback address.
- With a token, clients must send `Authorization: Bearer <token>` or get `401 Unauthorized`.

```bash
export MCP_TOKEN=$(openssl rand -hex 32)
./build/chatops-mcp serve --http 0.0.0.0:8080 --token-env MCP_TOKEN
```

The token only authenticates the caller; use a TLS-terminating proxy when the endpoint is reachable beyond a trusted network.

### Configuration directories

A tool group's `--config` names a directory, not a file. Every `*.yaml` in it is read in file name order and merged, so prefixes such as `00-` and `10-` set the order. An empty directory means no settings. (The daemon's own `config.yaml` is a single file.)

| Loaded | Skipped |
|---|---|
| names ending in `.yaml` that do not start with `.` | `.yml` files, hidden files, `.dpkg-dist` and `.rpmnew` leftovers, editor backups, subdirectories |

Other files, such as a kubeconfig, may sit in the directory as long as their names do not end in `.yaml`. A `kubeconfig.yaml` there would be loaded as group config and rejected for its unknown fields.

The packages install `/etc/chatops/mcp-status.d/`, `/etc/chatops/mcp-kubernetes.d/`, and `/etc/chatops/mcp-host.d/` (owned `root:chatops`, mode `0755`), each holding the shipped `00-default.yaml` (mode `0644` on a new install; an upgrade keeps an edited file's mode, and the deb keeps an unedited file's mode too). On upgrade:

| File | What the packages do |
|---|---|
| `00-default.yaml`, unedited | update it |
| `00-default.yaml`, edited | keep it, and save the new version beside it as `.dpkg-dist` or `.rpmnew`, which is skipped |
| files you add | never touch them |

So keep local settings in your own file (mode `0644`; secrets never belong in these directories), and leave `00-default.yaml` unedited to keep receiving the shipped defaults.

What a later file does depends on the kind of node it sets:

| Node in the later file | Result |
|---|---|
| mapping | Merged key by key: a key only in the later file is added, and a key in both is merged by these same rules. |
| list | Appended after the earlier items. Items are never matched or merged, so an entry repeating an earlier `name` is a second entry, which the group rejects as a duplicate. |
| scalar (string, number, boolean) | Replaces the earlier value: the last file wins. |
| empty or `null` | Ignored; the earlier value stays, so a later file cannot unset a value. |

For example, the shipped status list plus a file of your own, and a kubernetes setting overridden by another:

```yaml
# /etc/chatops/mcp-status.d/00-default.yaml        # /etc/chatops/mcp-status.d/10-homelab.yaml
services:                                          services:
  - name: github                                     - name: harbor
  - name: slack                                        display: Harbor
    ...                                                ...
# merged: github, slack, harbor (in that order)

# /etc/chatops/mcp-kubernetes.d/00-default.yaml    # /etc/chatops/mcp-kubernetes.d/10-site.yaml
kubeconfig: /etc/chatops/kubeconfig                max_results: 100
max_results: 50
# merged: kubeconfig: /etc/chatops/kubeconfig, max_results: 100
```

To change a shipped list entry, edit it in `00-default.yaml`; redefining it in another file adds a duplicate instead of replacing it.

Validation happens in two stages:

| Stage | Catches | Error names |
|---|---|---|
| Each file on its own | unknown fields, wrong types, a key that is a list in one file and a mapping or scalar in another | the file |
| The merged result | checks across files, such as duplicate names | the directory, with the index counted across the merged list in file name order |

For example, `status config /etc/chatops/mcp-status.d: services[9]: duplicate name "slack"` comes from a file of your own redefining the shipped `slack` entry; search the files for the quoted name.

- A relative path, such as `kubeconfig`, resolves against the config directory.
- YAML anchors and aliases work only within one file.

## Version

Both binaries share the same `version` subcommand:

```bash
./build/chatops version --all
./build/chatops version --json --all
```

| Flag | Description |
|---|---|
| `-j`, `--json` | Output in JSON format |
| `-a`, `--all` | Output all version details |
| `-b`, `--build-time` | Output build time |
| `-s`, `--source` | Output source of the executable |

## License

BSD 3-Clause, see [LICENSE](LICENSE).
