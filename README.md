# chatops

`chatops` connects a chat platform to an OpenAI-compatible language model and to tools served over the [Model Context Protocol](https://modelcontextprotocol.io/specification) (MCP). The model chooses which tools to call, ChatOps enforces authorization and approval, and the answer is posted back to the chat thread.

This project is under early development: `chatops chat` runs the agent in a terminal against a configured model and MCP servers, and `chatops-mcp` serves the `ping` tool. Slack integration, authorization policy, and approvals are not implemented yet.

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

```yaml
chat:
  slack:                               # Socket Mode app; the terminal harness ignores this
    bot_token_env: SLACK_BOT_TOKEN     # xoxb- bot token
    app_token_env: SLACK_APP_TOKEN     # xapp- app-level token with connections:write

llm:
  base_url: http://localhost:8080/v1   # OpenAI-compatible endpoint
  model: qwen3-0b6
  # api_key_env: LLM_API_KEY           # omit for keyless local endpoints
  disable_thinking: true

mcp:
  servers:
    builtin:                           # server ID: lowercase letters, digits, "-"
      transport: stdio
      command: ./build/chatops-mcp
      args: [serve, --tools, ping]
      # env:                           # plain values, stored in this file
      #   KUBECONFIG: /etc/chatops/kubeconfig
      # secret_env:                    # child variable: daemon variable holding the value
      #   VAULT_TOKEN: CHATOPS_VAULT_TOKEN
    # monitoring:
    #   transport: streamable-http
    #   url: https://monitoring.example.internal/mcp
    #   bearer_token_env: MONITORING_MCP_TOKEN

agent:
  max_iterations: 8
  turn_timeout: 120s
  tool_timeout: 30s
  max_tool_result_bytes: 65536
  history_turns: 20
  history_ttl: 24h
  max_concurrent_turns: 4
  max_pending_messages: 64
```

| Setting | Default | Description |
|---|---|---|
| `chat.slack.bot_token_env`, `app_token_env` | (none) | Environment variables holding the Slack bot (`xoxb-`) and app-level (`xapp-`) tokens; both are required when `chat.slack` is present |
| `llm.base_url` | (required) | OpenAI-compatible API base; requests go to `<base_url>/chat/completions` |
| `llm.model` | (required) | Model name sent with each request |
| `llm.api_key_env` | (none) | Environment variable holding the API key, sent as a bearer token; requires an `https` `base_url` unless the host is loopback |
| `llm.disable_thinking` | `false` | Send `reasoning_effort: "none"` and `chat_template_kwargs: {"enable_thinking": false}`; leave off for endpoints that reject unknown fields |
| `mcp.servers.<id>.transport` | (required) | `stdio` or `streamable-http` |
| `mcp.servers.<id>.command`, `args` | | stdio only: the server process to launch |
| `mcp.servers.<id>.env` | (none) | stdio only: extra environment variables; values are stored in the file, so never put secrets here |
| `mcp.servers.<id>.secret_env` | (none) | stdio only: maps a variable for the server to the `chatops` environment variable holding its value, for secrets such as tokens; a name may not appear in both `env` and `secret_env` |
| `mcp.servers.<id>.url`, `bearer_token_env` | | streamable-http only: endpoint and optional bearer token variable; a token requires an `https` URL unless the host is loopback |
| `agent.max_iterations` | `8` | Maximum model calls per turn |
| `agent.turn_timeout` | `120s` | Wall-clock limit for one turn; a turn that runs past it fails even if a late model or tool reply arrives |
| `agent.tool_timeout` | `30s` | Limit for one tool call; a timed-out call is reported to the model, which may continue; must not exceed `turn_timeout` |
| `agent.max_tool_result_bytes` | `65536` | Tool output beyond this is truncated before it reaches the model; a short truncation marker is appended on top of the limit |
| `agent.history_turns` | `20` | Earlier turns sent as context |
| `agent.history_ttl` | `24h` | A conversation idle this long (since its last answered message, failed or not) starts over with no history; the terminal harness ignores it |
| `agent.max_concurrent_turns` | `4` | Agent turns (model and tool calls) running at once across all conversations; posting a reply does not count; turns within one conversation always run one at a time, each after the previous reply is posted; the terminal harness ignores it |
| `agent.max_pending_messages` | `64` | Accepted but unfinished messages across all conversations; more are refused as busy; the terminal harness ignores it |

Each model-facing tool is named `<server>__<tool>`, for example `builtin__ping`. A stdio server inherits only `PATH` and `HOME` from `chatops`, plus its configured `env` and `secret_env`, so chat and model credentials never reach tool processes unless a `secret_env` entry names them. All servers must be reachable at startup.

## Chat in the terminal

`chatops chat` is a development harness: it runs the same agent loop a chat backend will use, with one conversation kept in memory. Type a message per line; `/quit`, `/exit`, or end of input stops it.

```bash
make build
./build/chatops chat --config config.yaml
```

```text
> is the builtin server reachable?
Yes, the builtin server replied pong.
>
```

| Flag | Default | Description |
|---|---|---|
| `-c`, `--config` | (required) | Path to the YAML config file |
| `--log-level` | `warn` | Minimum level of logs written to stderr: `debug`, `info`, `warn`, or `error`; `info` shows each tool call |

A failed turn (model unreachable, turn timeout) prints `error: ...` and the session continues; the failed turn is not added to history.

## Built-in MCP server

`chatops-mcp serve` exposes tool groups to any MCP client. By default it speaks MCP over stdio, which is how an MCP client normally launches it as a subprocess.

| Flag | Default | Description |
|---|---|---|
| `--tools` | `ping` | Comma-separated tool groups to serve |
| `--http` | (stdio) | Serve Streamable HTTP at `/mcp` on this address instead of stdio |
| `--token-env` | (none) | Environment variable holding the bearer token HTTP clients must send; required when `--http` is not a loopback address |

| Tool group | Tools | Description |
|---|---|---|
| `ping` | `ping` | Takes no arguments and replies `pong`; read-only, for smoke tests |

To exercise it by hand over stdio, send an MCP `initialize` handshake followed by a tool call. The trailing `sleep` keeps stdin open until the replies are written:

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
