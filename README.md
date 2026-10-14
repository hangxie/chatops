# chatops

`chatops` connects a chat platform to an OpenAI-compatible language model and to tools served over the [Model Context Protocol](https://modelcontextprotocol.io/specification) (MCP). The model chooses which tools to call, ChatOps enforces authorization and approval, and the answer is posted back to the chat thread.

This project is under early development: `chatops serve --chat <names>` answers messages from the selected chat backends (Slack, or a local terminal), and `chatops-mcp` serves the `ping`, `service_status`, and read-only Kubernetes node tools (`node_list`, `node_status`, `node_resources`). Authorization policy and approvals are not implemented yet.

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

## Configuration

`chatops` reads a single YAML file holding exactly one YAML document; a second document (after `---`) is an error rather than silently ignored. Unknown fields are rejected and every invalid field is reported at startup. Secrets never appear in the file: settings ending in `_env` name the environment variable that holds the secret, and a named variable that is unset or empty is an error. A bearer token or API key is only sent over `https`, or plain `http` to a loopback host (`localhost`, `127.0.0.0/8`, `::1`); any other `http` URL combined with `api_key_env` or `bearer_token_env` is rejected.

The annotated sample at [`package/systemd/config.yaml`](package/systemd/config.yaml) is the canonical, runnable reference: it carries every setting with inline comments and is verified by the test suite. Copy it as a starting point; the table below is the authoritative explanation of each setting.

| Setting | Default | Description |
|---|---|---|
| `chat.<name>.type` | (required) | Chat backend kind: `slack`, `stdio`, or `unix`; `<name>` is a free-form id selected on the command line with `--chat` |
| `chat.<name>.bot_token_env`, `app_token_env` | (none) | slack only: environment variables holding the Slack bot (`xoxb-`) and app-level (`xapp-`) tokens; both required |
| `chat.<name>.path`, `mode` | (none) | unix only: filesystem path of the socket to create (required) and optional octal permission bits (e.g. `0600`, the default owner-only) |
| `llm.base_url` | (required) | OpenAI-compatible API base; requests go to `<base_url>/chat/completions` |
| `llm.model` | (required) | Model name sent with each request |
| `llm.api_key_env` | (none) | Environment variable holding the API key, sent as a bearer token; requires an `https` `base_url` unless the host is loopback |
| `llm.disable_thinking` | `false` | Send `reasoning_effort: "none"` and `chat_template_kwargs: {"enable_thinking": false}`; the packaged sample turns it on; leave off for endpoints that reject unknown fields |
| `llm.sampling.*` | (unset) | Optional generation parameters, each sent only when set so an unset one keeps the endpoint default: `temperature` (0–2), `top_p` (0–1), `max_tokens` (>0), `frequency_penalty`/`presence_penalty` (−2–2), `seed`, and `stop` (list of strings). `top_k` (≥0), `min_p` (0–1), and `repetition_penalty` (>0) are vLLM/llama.cpp extensions that strict OpenAI endpoints reject. `temperature: 0` is greedy and deterministic, the most reliable choice for an ops assistant |
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

Each model-facing tool is named `<server>__<tool>`, for example `builtin__ping`. A stdio server inherits only `PATH` and `HOME` from `chatops`, plus its configured `env` and `secret_env`, so chat and model credentials never reach tool processes unless a `secret_env` entry names them. All servers must be reachable at startup.

### Conversation history

`agent.history_turns` sets how many of a conversation's earlier user messages are replayed as context on each new message. Only user messages are replayed — never assistant answers, tool calls, or tool results — so an earlier reply can never bias a later one. It is a direct trade-off between conversational feel and per-answer accuracy, and the right value depends on your model.

This is about isolation, not factual accuracy: how correct an answer is still comes down to the tools, the model, and the prompt. History only controls how much of the earlier conversation the model can see — and therefore drag into — the current reply.

`0` (the default) sends no history: every message is answered in isolation, so an unrelated earlier question cannot bleed into the reply and the answer stays on exactly what was asked. The trade-off is no memory — a follow-up like "check it again" or "how about Slack?" has no referent and will be misread or refused. Prefer this for one-shot status questions.

`1` replays only the immediately preceding user message. Short elliptical follow-ups resolve ("and Claude Code?" → the model knows you were asking about status), while the model rarely drags in older topics. This is the best starting point if you want follow-ups to work; on small models (for example a 1.5–2B parameter model) it occasionally still restates the previous subject.

Higher values (`3`, `5`, …) feel the most conversational and let references reach several turns back, but each extra turn is one more past question in the prompt with no marker that it was already answered, so smaller models increasingly pile up unrelated systems into every reply and drift off the current question. Larger, more instruction-following models tolerate higher values better. Raise it gradually and watch for answers that report things you did not ask about.

Whatever the value, a strong `system_prompt` helps — the packaged prompt makes the model decide first whether a message is an operational request its tools can cover, decline anything else (greetings and small talk included) with a fixed sentence rather than engaging, and otherwise answer only the latest request, resolving implicit references from history. History older than `agent.history_ttl` is dropped, so an idle conversation starts fresh.

## Chat backends

`chatops serve` runs the daemon against one or more named chat backends. Backends are declared in the `chat:` map, each with a `type` and a free-form name, and `--chat` selects which ones to run (comma-separated, required, no default). Several can run at once, all sharing one agent and conversation manager. See [`docs/chat.md`](docs/chat.md) for the design.

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
| `--chat` | (required) | Comma-separated backend names from the `chat:` map (e.g. `--chat eng-slack` or `--chat eng-slack,console`) |
| `--log-level` | `info` | Minimum level of logs written to stderr: `debug`, `info`, `warn`, or `error`; `debug` adds each tool call's arguments; see [Debug logs](#debug-logs) |
| `--log-format` | `text` | Log format on stderr: `text` or `json` |

Startup aborts if a selected backend is absent from the config or invalid. On shutdown, in-flight turns and their replies are given a short grace period to finish before the daemon exits.

### `stdio` — interactive terminal

The `stdio` backend is an interactive development harness: one conversation on stdin/stdout, so it needs a terminal and is not for an unattended daemon (under systemd it reads immediate EOF and exits). Type a message per line; `/quit`, `/exit`, or end of input stops it. A failed turn (model unreachable, turn timeout) prints `error: ...` and the session continues.

```bash
make build
./build/chatops serve --config config.yaml --chat console
```

```text
> is the builtin server reachable?
Yes, the builtin server replied pong.
>
```

### `slack` — Socket Mode

The `slack` backend answers Slack messages over Socket Mode, so it needs no public endpoint.

1. At https://api.slack.com/apps, choose "Create New App", then "From an app manifest", and paste `scripts/slack-app-manifest.json`; edit the name first if you want a different bot name.
2. Install the app to the workspace and export its `xoxb-` bot token; generate an app-level `xapp-` token with the `connections:write` scope and export it too. The variable names are whatever the backend's `bot_token_env` and `app_token_env` say.
3. Start the daemon:

```bash
export SLACK_BOT_TOKEN=xoxb-... SLACK_APP_TOKEN=xapp-...
./build/chatops serve --config config.yaml --chat eng-slack
```

### `unix` — local socket

The `unix` backend lets a client on the same host reach a running daemon over a Unix domain socket: one conversation per connection, a message per line. Access control is the socket file's permissions (`mode`, default owner-only), so it needs no token; it is the right way to drive a daemon locally, unlike `stdio`. A live socket refuses a second daemon's startup, and an exclusive lock file (`<path>.lock`) held for the daemon's lifetime keeps two instances from racing on the same path. For example, with a backend named `ops` of `type: unix` and `path: /run/chatops/ops.sock`:

```bash
./build/chatops serve --config config.yaml --chat homelab,ops   # Slack plus the local socket
nc -U /run/chatops/ops.sock                                      # then talk to it, one message per line
```

### Debug logs

At `--log-level debug`, `serve` logs a `tool call requested` line with the arguments the model passed to each tool, up to 1 KiB, which shows which tool and inputs the model chose:

```text
level=DEBUG msg="tool call requested" tool=builtin__service_status arguments="{\"service\": \"openai\"}"
```

Turn it on only while troubleshooting. Credentials never appear there: Slack tokens, the LLM API key, and MCP tokens and `secret_env` values are read by the processes themselves and never reach the model. The arguments can, however, repeat anything the model has seen, including what users typed (such as a password pasted into a question) and earlier tool results. At `info` and above, `serve` logs Slack user and conversation IDs but never message text. Under systemd, logs go to the journal, which is usually readable by the `adm` and `systemd-journal` groups, a wider audience than `/etc/chatops/chatops.env`.

How it answers:

- In a channel the app has been invited to, a message must start with a mention of the bot, such as `@chatops is the builtin server up?`; a mention later in the message is ignored. Follow-ups in the thread need the mention too.
- In a direct message to the app, every message is answered; no mention is needed.
- Replies go into the message's thread. Each thread is a separate conversation with its own history, so context never crosses threads; a direct message that is not in a thread starts a new conversation.
- Messages in one thread are answered one at a time, in order; different threads are answered concurrently up to `agent.max_concurrent_turns`. When `agent.max_pending_messages` is reached, new messages get a short "try again shortly" reply; at most four such replies are posted at a time, and further refused messages are only logged.
- Slack retries and duplicate deliveries of an accepted message are answered once; a message refused as busy may be accepted if Slack delivers it again.
- A failed turn gets a short apology in the thread; the details, which may include internal endpoints, go only to the log.
- Replies are posted as plain text with Slack link previews turned off, so model output cannot mention `@channel` or users, or make Slack fetch a URL.
- Every message is answered with every configured tool available; per-user permissions come in a later release, and `serve` logs a warning at startup until then. Until that release, configure only read-only tools, and only invite the app where all members may use all of its tools.
- Stopping the daemon (SIGINT or SIGTERM) stops accepting new messages and gives in-flight turns and their replies a short grace period to finish before exiting; anything still running past it is cancelled, and conversation history is lost.

### Run as a service

The deb and rpm packages install both binaries, a `chatops` system user, and a systemd unit that runs `chatops serve` as that user:

| Path | Purpose |
|---|---|
| `/etc/chatops/config.yaml` | Config file; set `llm.base_url` and `llm.model` before starting |
| `/etc/chatops/status.yaml` | Services the `status` tool group checks; edit to add services or product names |
| `/etc/chatops/chatops.env` | Secrets (`SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`, optional `LLM_API_KEY`) and `LOG_LEVEL`/`LOG_FORMAT`; readable only by root and the `chatops` group |
| `chatops.service` | Runs `chatops serve --config /etc/chatops/config.yaml --chat ${CHAT}` (default `homelab`, the backend named in the sample config) with JSON logs to the journal; restarts on failure |

Both files are kept across upgrades. The service is not started on install; once both files are filled in:

```bash
sudo systemctl enable --now chatops
journalctl -u chatops -f
```

## Built-in MCP server

`chatops-mcp serve` exposes tool groups to any MCP client. By default it speaks MCP over stdio, which is how an MCP client normally launches it as a subprocess.

| Flag | Default | Description |
|---|---|---|
| `--tools` | `ping` | Comma-separated tool groups to serve |
| `--http` | (stdio) | Serve Streamable HTTP at `/mcp` on this address instead of stdio |
| `--status-config` | (none) | YAML file listing the services the `status` group checks; required with `--tools status` |
| `--kubeconfig` | (none) | Path to the kubeconfig for the `kubernetes` group, honored exactly with no fallback; requires `--tools kubernetes` |
| `--kube-context` | (none) | Kubeconfig context for the `kubernetes` group; forces kubeconfig-based resolution; requires `--tools kubernetes` |
| `--token-env` | (none) | Environment variable holding the bearer token HTTP clients must send; required when `--http` is not a loopback address |

### Built-in MCPs

Each tool group has a design doc under [`docs/`](docs/); the overall architecture is [`docs/DESIGN.md`](docs/DESIGN.md).

| Tool group | Tools | Purpose | Design |
|---|---|---|---|
| `ping` | `ping` | Connectivity smoke test; replies `pong` | [`docs/mcp-ping.md`](docs/mcp-ping.md) |
| `status` | `service_status` | Public status and active incidents of external services, from their status pages | [`docs/mcp-status.md`](docs/mcp-status.md) |
| `kubernetes` | `node_list`, `node_status`, `node_resources`, `namespace_list`, `deployment_list`, `deployment_pods`, `deployment_describe`, `pod_list`, `pod_describe`, `pod_log`, `pod_resources`, `statefulset_list`, `statefulset_pods`, `statefulset_describe`, `daemonset_list`, `daemonset_pods`, `daemonset_describe`, `replicaset_list`, `replicaset_pods`, `replicaset_describe`, `service_list`, `service_pods`, `service_describe`, `job_list`, `job_pods`, `job_log`, `job_describe` | Read-only Kubernetes inspection: node roster, health, and resource capacity/utilization; namespaces and why one is stuck terminating; deployment health, pods, and definition; pod roster, definition, logs, and resource usage; statefulset health, pods, and definition; daemonset health, pods, and definition; replicaset health, pods, and definition; service health, backing pods, and definition; job status, pods, logs, and definition | [`docs/mcp-k8s.md`](docs/mcp-k8s.md) |

`node_status` and `node_resources` take a `nodes` list, so a question about several nodes is one call: `node_list` for the names, then one call with all of them. Every other per-object tool likewise takes a list named after its resource (`pods`, `deployments`, and so on), of `{namespace, name}` objects for namespaced resources. A list holds at most 50 distinct targets (5 for every `_describe` and `_log` tool, whose output is a full object or raw container output); a target that does not exist is reported as not found without failing the rest, and a namespace that does not exist is named as such.

The `kubernetes` group connects with `--kubeconfig`/`--kube-context`, else in-cluster credentials when running as a pod, else the default local kubeconfig. See [`docs/mcp-k8s.md`](docs/mcp-k8s.md) for the connection model. Node utilization needs metrics-server; without it, capacity and allocatable are still reported. The group needs only read-only RBAC:

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
- apiGroups: ["metrics.k8s.io"]
  resources: [nodes, pods]
  verbs: [get, list]
```

This role is read-only, but its reads are cluster-wide: the credentials can read pod metadata, workload pod templates, and container logs in every namespace. The `_describe` tools redact literal environment values before they reach chat (values of 12 or more characters keep their last 3), but secrets passed in container `command` or `args`, and anything a container writes to its logs, are not redacted, and the grant itself is the real boundary. Scope the binding or namespaces accordingly if that is broader than intended.

`node_resources` reports CPU/memory/disk/pod capacity and allocatable from the node API, CPU and memory utilization from the metrics API, and the pod count from the pod API. Node disk utilization comes from the kubelet Summary API and is reported only when the credentials also have `get` on `nodes/proxy`; without it, each node notes that disk utilization is not permitted:

```yaml
- apiGroups: [""]
  resources: [nodes/proxy]
  verbs: [get]
```

`nodes/proxy` is a powerful permission that reaches every kubelet endpoint, not just the Summary API, so grant it only when node disk reporting is wanted. The Summary API reports the kubelet's own filesystem usage, not arbitrary host-mounted disks (a separate data disk or Ceph OSD will not appear); use Prometheus with node-exporter for comprehensive host disk monitoring.

The `status` group has no built-in services: every service comes from the `--status-config` file. `package/systemd/status.yaml` is the shipped list, installed as `/etc/chatops/status.yaml` and kept across upgrades; edit it to add services or product names and restart the server. See [`docs/mcp-status.md`](docs/mcp-status.md) for the entry schema and supported page types.

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

Per-object Kubernetes tools take their targets as a list; with `--tools kubernetes`, this third line reports several nodes in one call:

```text
'{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"node_resources","arguments":{"nodes":["master-0","master-1"]}}}'
```

To serve over HTTP instead, run `./build/chatops-mcp serve --http 127.0.0.1:8080` and point an MCP client at `http://127.0.0.1:8080/mcp`. Press Ctrl+C to stop.

The HTTP endpoint has no authentication unless `--token-env` names a variable holding a bearer token, so `serve` refuses to listen on anything other than a loopback address without one. With a token, clients must send `Authorization: Bearer <token>` or get `401 Unauthorized`:

```bash
export MCP_TOKEN=$(openssl rand -hex 32)
./build/chatops-mcp serve --http 0.0.0.0:8080 --token-env MCP_TOKEN
```

The token only authenticates the caller; use a TLS-terminating proxy when the endpoint is reachable beyond a trusted network.

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
