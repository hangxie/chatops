# ChatOps v2: Architecture

Status: accepted design. Repository: https://github.com/hangxie/chatops.

This is the conclusions-only design of record. It describes the accepted target architecture, not only what is built today; features not yet implemented are identified in the Status section (§11). Per-MCP designs live beside it: [`mcp-ping.md`](mcp-ping.md), [`mcp-status.md`](mcp-status.md), [`mcp-k8s.md`](mcp-k8s.md); the chat backend design is [`chat.md`](chat.md); per-reply stats and Prometheus metrics are in [`observability.md`](observability.md). Usage lives in [`../README.md`](../README.md).

## 1. Summary

ChatOps is a Go service that connects a chat platform to an OpenAI-compatible language model and to tools served over the Model Context Protocol (MCP). The model reasons and selects actions; ChatOps owns conversation orchestration, authorization, approval, and auditability, but defines no proprietary tool protocol.

The repository ships two binaries: the `chatops` daemon (an MCP client) and `chatops-mcp`, a standalone built-in MCP server providing first-party tools. The daemon talks to `chatops-mcp` exactly as it talks to any third-party MCP server; there is no in-process shortcut.

## 2. Goals

1. Use standard MCP for tool discovery and invocation; define no ChatOps-specific tool wire protocol.
2. Support multi-step, bounded LLM tool calling with tool results fed back into subsequent model requests.
3. Use Slack Socket Mode as the primary chat interface, preserving thread context.
4. Support OpenAI-compatible model APIs, including local endpoints.
5. Connect to explicitly configured MCP servers over stdio and Streamable HTTP via the official Go MCP SDK.
6. Authorize before exposing tools to the model and again before executing a call; support explicit approval for sensitive actions.
7. Ship first-party tools as a standalone MCP server usable by any MCP client.
8. Keep the core small, testable, observable, and deployable in a private environment.

## 3. Non-goals

- Backward compatibility with any earlier chat, planner, tool, or config interface.
- A built-in custom tool framework, registration protocol, or plugin system.
- Serving MCP from inside the daemon process; first-party tools live in `chatops-mcp`.
- Production chat integrations other than Slack; a terminal adapter exists for development only.
- Unattended production mutations by default.
- Durable conversation storage; history is in memory and lost on restart.
- Model response streaming; replies are posted as final threaded messages.
- MCP capabilities beyond tools (prompts, resources, elicitation, sampling).

## 4. High-level architecture

```text
            Slack (Socket Mode)        terminal (dev only)
                    \                     /
                     v                   v
                       chat adapter
                            |
                            v
                   conversation manager
                            |
                            v
                    agent turn loop <----> LLM adapter
                            |
                       policy engine  <----> approvals (Slack buttons)
                            |
                       MCP manager
                       /         \
                      v           v
           chatops-mcp (stdio)   third-party server (HTTP) ...

          conversation store / audit log / telemetry
```

Ownership boundaries:

- **Chat adapter:** authenticates inbound events, normalizes messages, tracks thread and user identity, sends replies and approval prompts, and delivers approval clicks with the verified clicker identity.
- **Conversation manager:** serializes turns per conversation while running different conversations concurrently, keeps bounded in-memory history, and holds suspended turns awaiting approval.
- **Agent:** builds model context, advertises the authorized tool snapshot, runs a bounded sequence of model and tool calls, and returns the model's final text. The turn deadline is checked after every model completion and every tool call; a per-tool timeout is reported to the model as a tool error and the turn continues; `max_tool_result_bytes` bounds tool output.
- **LLM adapter:** handles OpenAI-compatible request/response shapes, tool calling, cancellation, and local-model quirks (such as disabling thinking).
- **MCP manager:** manages sessions and transports, discovers tools, maps model-facing names to server/tool identities, and handles reconnects, timeouts, and catalog changes.
- **Policy and approval:** decides which tools a requester may see or invoke, whether a call needs approval, and who may approve; records each decision.
- **Audit/observability:** records event IDs, requester, tool identity, decisions, timings, and error categories with secrets redacted.

## 5. Request lifecycle

1. Receive a Slack event, let the Slack SDK verify it, deduplicate retries by event ID, and map it to a conversation key.
2. Load conversation history and identify the requester (Slack user ID) and workspace/channel.
3. Take a per-turn snapshot of the tool catalog filtered by the requester's policy; disallowed tools are never shown to the model.
4. Send the system prompt, history, user message, and authorized tool schemas to the LLM. The schemas sent drop `null` from type lists such as `["null","array"]`, which the Go MCP SDK emits for every slice and which some models answer with a JSON-encoded string; arguments are still validated against the server's original schema. The cost is that the model no longer sees which fields accept an explicit `null`; it can still omit optional ones.
5. Plain text from the LLM is the final answer.
6. Tool calls are processed sequentially: resolve the model-facing name against the turn snapshot, validate arguments against the tool's JSON Schema, and enforce policy again.
7. A call needing approval suspends the turn and posts an approval prompt showing the exact server, tool, and arguments.
8. Invoke the MCP tool with a per-call timeout; treat output as untrusted data, never as instructions.
9. Append a truncated result and continue until the model answers or a limit is reached.
10. Reply in the originating thread, update history, and emit audit and telemetry records.

Per-turn limits bound model/tool iterations, wall-clock time, tool-result bytes, token budget, and per-tool time. Hitting a limit or a failed call produces a readable reply, not a retry loop; validation failures and policy denials are fed back to the model as tool errors so it can correct itself. A model that ignores the error and requests an identical call (same tool and arguments) that failed twice in a row stops the turn instead of spending the remaining iterations.

## 6. MCP integration

### 6.1 Client (`internal/mcp`)

The client uses the official Go SDK with its native transports and lifecycle negotiation (no hand-written JSON-RPC). The manager is a thin wrapper that:

- Connects only to administrator-configured servers, over stdio subprocesses or Streamable HTTP.
- Validates arguments against the snapshot tool's schema at `Manager.Call` and sends exactly the validated object; it does not authorize (Phase 4 enforces policy at the same boundary).
- Sends `bearer_token_env` as `Authorization: Bearer` for authenticated HTTP clients and refuses every redirect.
- Discovers tools via `tools/list` with pagination and calls them via `tools/call`, preserving `isError`, text content, and `structuredContent`.
- Names model-facing tools `<server>__<tool>`, sanitized to `^[a-zA-Z0-9_-]{1,64}$`, truncating with a short hash on collision or overflow; the reverse mapping is exact.
- Refreshes the catalog on reconnect and on `notifications/tools/list_changed`; a turn keeps its snapshot, and a tool that vanished mid-turn returns a tool error.
- Keeps server credentials out of model context and logs; stdio servers inherit only `PATH` and `HOME` plus their configured `env`.

Tool descriptions, schemas, annotations, and outputs from any server are untrusted. Annotations such as `readOnlyHint` and `destructiveHint` may only make policy stricter, never grant access or waive approval; without an administrator rule, every tool is treated as potentially mutating.

### 6.2 Built-in server (`cmd/chatops-mcp`, `pkg/mcpserver`)

`chatops-mcp` is a standalone MCP server built with the same SDK. It serves stdio by default and Streamable HTTP when asked. Over HTTP, `--token-env NAME` requires a static bearer token and `serve` refuses a non-loopback bind without one; TLS is left to a fronting proxy. Tool groups live under `pkg/mcpserver/<group>` as public packages, wired explicitly in `cmd/chatops-mcp` (no `init()` or blank-import registration). Each group declares honest `readOnlyHint`/`destructiveHint` annotations even though the daemon does not trust them for granting access.

Implemented groups: [`ping`](mcp-ping.md), [`status`](mcp-status.md), and [`kubernetes`](mcp-k8s.md) (read-only node and deployment tools; Phase 3).

## 7. LLM and conversation

The LLM adapter exposes one request/response pair: a request with messages and tool specs, a response with either assistant text or tool calls. The first implementation targets OpenAI chat-completions with `tool_choice: auto`; there is no reply tool and no forced tool call, because plain assistant text is the answer. The adapter strips a leading `<think>...</think>` block, assigns `call_<index>` IDs to tool calls that lack one, and fails a completion on `finish_reason` of `length` or `content_filter` (a missing `finish_reason` is accepted). When `api_key_env` is set it refuses every redirect. Optional sampling parameters are sent only when configured; otherwise the endpoint default applies.

The conversation key is workspace + channel + thread timestamp. History is in memory, bounded by age and turn count, oldest-first; only earlier user messages are replayed, never assistant answers, tool calls, or tool results. Messages from other threads are never included. Tool execution is sequential even when the model requests several calls at once.

## 8. Authorization, approval, and security

Default deny. The effective tool set is the intersection of configured servers, policy rules, and the requester's roles.

- **Identity and roles:** the requester is the Slack user ID from the verified event, never message text. Roles come from a static config map of Slack user IDs to role names. Unknown or undeterminable identity fails closed.
- **Policy rules:** a rule names a server, a tool (exact or `*`), the allowed roles, and whether approval is required. Matching is on stable server/tool identity. A destructive annotation forces approval regardless of the rule.
- **Approval:** a call needing approval suspends the turn and posts Approve/Deny buttons. An approval is bound to server ID, MCP tool name, SHA-256 of canonical JSON arguments, requester, conversation key, and expiry; changing any argument invalidates it. On a click the daemon verifies the clicker's identity and approver role, checks expiry and binding, rechecks policy, then resumes. Self-approval is off by default. Suspended turns are lost on restart.
- **General controls:** the model can never add a server, launch a command, choose an endpoint, or alter credentials — those come only from config. Downstream controls (Kubernetes RBAC, Vault policies) remain enforced independently. Authoritative instructions and authorization stay outside untrusted content (chat text, tool metadata, tool results). Secrets are redacted from logs.
- `chatops-mcp` HTTP bearer tokens authenticate callers but do not authorize them. Tool-group selection (`--tools`), downstream RBAC, and the credentials a deployment hands the process are three separate controls, each scoped on its own; ChatOps policy sits in front of all of them. Unauthenticated loopback HTTP trusts every local process on the host, so privileged groups on shared hosts must use `--token-env` or stdio.

## 9. Configuration

A single YAML file passed with `--config`, validated at startup, holding exactly one document. Secrets never appear in YAML; fields ending in `_env` name the environment variable holding the secret, and stdio servers get secrets through `secret_env` rather than plaintext `env`. Bearer tokens and API keys are only configured with `https` URLs or plain `http` to a loopback host. The annotated, runnable reference is [`../package/systemd/config.yaml`](../package/systemd/config.yaml); the README settings table is the authoritative per-field explanation.

## 10. Repository layout

```text
chatops/
├── cmd/
│   ├── chatops/               # daemon binary: main.go + kong CLI (serve)
│   └── chatops-mcp/           # built-in MCP server binary
├── internal/
│   ├── agent/                 # bounded LLM/tool turn loop
│   ├── chat/                  # chat backend interface, factory, runner; slack and stdio backends
│   ├── conversation/          # per-thread serialization, history, suspended turns
│   ├── llm/                   # OpenAI-compatible adapter
│   ├── mcp/                   # SDK client wrapper, catalog, name mapping
│   ├── policy/                # roles, rules, approval binding
│   ├── config/                # YAML parsing and validation
│   ├── command/               # shared kong parser, Run, signal-aware context
│   ├── version/               # shared `version` subcommand
│   └── testutils/
├── pkg/
│   └── mcpserver/             # ping, status, kubernetes tool groups
├── package/                   # rpm/deb/container packaging; systemd unit
├── docs/                      # this design and the per-MCP designs
├── Makefile
├── AGENTS.md                  # CLAUDE.md symlinks here
└── README.md
```

There is no top-level `main.go`; `main` packages live under `cmd/<binary>/`, and kong subcommands nest at `cmd/<binary>/<subcommand>/` with a `Cmd` struct and `Run()` method. Subcommands shared by both binaries live in `internal/<name>/` (currently only `version`). Favor concrete types until a second implementation or a testing seam justifies an interface.

## 11. Status

- **Phase 0 — bootstrap:** done. Both binaries build and package (deb/rpm/container); main branch protection applied (required `build / build` check, signed commits, linear history, one approving review).
- **Phase 1 — local vertical slice:** done. `chatops-mcp serve` + `ping`, `internal/config`, `internal/mcp`, `internal/llm`, `internal/agent`, and the `stdio` terminal chat backend. Verified live against `http://media.xiehang.com:8080/v1` with `qwen3-1.7b-q8`.
- **Phase 2 — Slack:** done. Socket Mode, threading, retry deduplication, per-thread serialization. Chat backends are named and selected with `--chat`; `slack` and `stdio` ship, `unix`/`http`/`tcp` planned (`udp` deferred). See [`chat.md`](chat.md).
- **Phase 2.5 — service status:** done. The `status` group.
- **Phase 3 — Kubernetes:** node slice done (`node_list`, `node_status`, `node_resources`, read-only); deployment slice done (`deployment_list`, `deployment_pods`, `deployment_describe`). See [`mcp-k8s.md`](mcp-k8s.md).
- **Phase 4 — policy and approval:** not started. Roles, rules, execution-time checks, suspended turns, Slack approval buttons; a startup warning is logged until policy exists, so until then deploy only read-only tools or fully trusted channels.
- **Phase 5 — hardening:** metrics done (per-reply stats and Prometheus, see [`observability.md`](observability.md)); reconnect handling, structured audit events, and integration tests remain.

## 12. Testing strategy

- Unit tests (table-driven, testify `require`) for name mapping, argument hashing, approval binding, policy matching, conversation isolation, budget enforcement, and config validation.
- MCP contract tests using the SDK's in-memory transport against `pkg/mcpserver` groups: discovery, pagination, success/error results, list changes, timeout, reconnect, malformed responses.
- Agent tests against a deterministic fake LLM: plain reply, sequential tool calls, invalid arguments, denied call, approval suspend/resume, prompt injection in tool output, iteration exhaustion, cancellation.
- End-to-end smoke tests against a live model and cluster, run manually or behind a build tag.

## 13. Decisions

1. Slack is the only production chat backend; a terminal adapter exists for development.
2. Conversation history and suspended turns are in memory only.
3. The first real integration is read-only Kubernetes via the built-in `chatops-mcp`.
4. Trust model: static Slack user ID to roles map, with per-tool rules.
5. Approval happens in Slack via buttons, by users holding the rule's approver role; self-approval is off by default.
6. No streaming; final replies in thread.
7. First deployment target is systemd using the rpm/deb packaging covering both binaries.

## 14. References

- MCP specification: https://modelcontextprotocol.io/specification
- Official MCP Go SDK: https://github.com/modelcontextprotocol/go-sdk
