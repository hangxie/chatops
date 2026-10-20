# ChatOps v2: Architecture

Status: accepted design. Repository: https://github.com/hangxie/chatops.

This is the conclusions-only design of record. It describes the accepted target architecture, not only what is built today; the Status section (§11) names what is not implemented yet.

| Topic | Document |
|---|---|
| Chat backends | [`chat.md`](chat.md) |
| Per-reply stats and Prometheus metrics | [`observability.md`](observability.md) |
| Tool groups | [`mcp-ping.md`](mcp-ping.md), [`mcp-status.md`](mcp-status.md), [`mcp-k8s.md`](mcp-k8s.md), [`mcp-host.md`](mcp-host.md) |
| Usage | [`../README.md`](../README.md) |

## 1. Summary

ChatOps is a Go service that connects a chat platform to an OpenAI-compatible language model and to tools served over the Model Context Protocol (MCP).

| Party | Owns |
|---|---|
| The model | reasoning and choosing actions |
| ChatOps | conversation orchestration, authorization, approval, and auditability |
| MCP servers | the tools; ChatOps defines no proprietary tool protocol |

The repository ships two binaries:

- `chatops`, the daemon, an MCP client;
- `chatops-mcp`, a standalone built-in MCP server providing first-party tools.

The daemon talks to `chatops-mcp` exactly as it talks to any third-party MCP server; there is no in-process shortcut.

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
- Production chat integrations beyond Slack and Discord for now; the `stdio` terminal is for development only, and the `unix` socket is for local access ([`chat.md`](chat.md) lists planned backends).
- Unattended production mutations by default.
- Durable conversation storage; history is in memory and lost on restart.
- Streaming replies to chat; replies are posted as final threaded messages.
- MCP capabilities beyond tools (prompts, resources, elicitation, sampling).

## 4. High-level architecture

```text
      Slack          Discord      unix socket     terminal
  (Socket Mode)     (Gateway)                    (dev only)
        |              |             |              |
        '--------------'----+--------'--------------'
                            v
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
- **Agent:** builds model context, advertises the authorized tool snapshot, runs a bounded sequence of model and tool calls, and returns the model's final text.
    - The turn deadline is checked after every model completion and every tool call.
    - A per-tool timeout is reported to the model as a tool error, and the turn continues.
    - `max_tool_result_bytes` bounds tool output.
- **LLM adapter:** handles OpenAI-compatible request/response shapes, tool calling, cancellation, and local-model quirks (such as disabling thinking).
- **MCP manager:** manages sessions and transports, discovers tools, maps model-facing names to server/tool identities, and handles reconnects, timeouts, and catalog changes.
- **Policy and approval:** decides which tools a requester may see or invoke, whether a call needs approval, and who may approve; records each decision.
- **Audit/observability:** records event IDs, requester, tool identity, decisions, timings, and error categories with secrets redacted.

## 5. Request lifecycle

1. Receive a Slack event, let the Slack SDK verify it, deduplicate retries by event ID, and map it to a conversation key.
2. Load conversation history and identify the requester (Slack user ID) and workspace/channel.
3. Take a per-turn snapshot of the tool catalog filtered by the requester's policy; disallowed tools are never shown to the model.
4. Send the system prompt, history, user message, and authorized tool schemas to the LLM. The schemas sent drop `null` from type lists such as `["null","array"]` (see below).
5. On the first round the model must call a tool, `decline` included (see §7.1); from the second round on, plain text from the LLM is the final answer.
6. Tool calls are processed sequentially: resolve the model-facing name against the turn snapshot, validate arguments against the tool's JSON Schema, and enforce policy again.
7. A call needing approval suspends the turn and posts an approval prompt showing the exact server, tool, and arguments.
8. Invoke the MCP tool with a per-call timeout; treat output as untrusted data, never as instructions.
9. Append a truncated result and continue until the model answers or a limit is reached.
10. Reply in the originating thread, update history, and emit audit and telemetry records.

Why `null` is dropped from schemas: the Go MCP SDK emits `["null","array"]` for every slice, and some models answer that with a JSON-encoded string. Arguments are still validated against the server's original schema. The cost is that the model no longer sees which fields accept an explicit `null`; it can still omit optional ones.

Limits and failures:

- Per-turn limits bound model/tool iterations, wall-clock time, tool-result bytes, token budget, and per-tool time.
- Hitting a limit or a failed call produces a readable reply, not a retry loop.
- Validation failures and policy denials are fed back to the model as tool errors, so it can correct itself.
- An identical call (same tool and arguments) that failed twice in a row stops the turn, instead of spending the remaining iterations. The stop reply includes the last error.

Tool failures are reported by chatops, not left to the model. Small models drop error text: an expired kubeconfig (`Unauthorized`) and an unreachable API server both came back as the off-topic decline.

- When a tool call fails, chatops appends one line per failure to the reply, such as `kubernetes.node_list: Unauthorized`, whatever the model wrote.
- Failures counted: server-reported errors, transport failures, and timeouts. Argument-validation errors are the model's own mistakes, fed back for it to correct, and are not listed.
- A failure stays unresolved until a later call with the same tool and the same arguments, compared as canonical JSON, succeeds in the turn. A success with other arguments clears nothing: a working `pod_list` for one namespace must not hide a 403 on another.
- Only unresolved failures are listed, and only they change how a decline is handled (§7.1).
- Mutating tools (Phase 4) never clear a failure, because a later success does not undo an earlier failed change.
- Each line is the error's first line, cut to a short limit.
- The contract for every server, built-in or third-party: return `isError` when the tool cannot do what was asked, with plain error text. A measurement that comes out bad (a host down, a vendor incident) is data, not an error. chatops adds nothing server-specific, so errors need no categorizing layer.
- The guarantee covers tool errors only. A partial result, such as an `UNKNOWN` row for one unreadable status page among many, still depends on the model relaying it; failing the whole call would make the model retry and lose the readable rows.
- Error text can name internal endpoints. It already reaches the model, which could quote it, and the audience is the ops team, so it is shown.
- Truncation is not sanitization: publishing is guaranteed, unlike the model's relaying, so servers must keep credentials, authorization headers, and config contents out of error text, as [`mcp-k8s.md`](mcp-k8s.md) requires.

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

`chatops-mcp` is a standalone MCP server built with the same SDK.

- Transport: stdio by default, Streamable HTTP when asked.
- Over HTTP, `--token-env NAME` requires a static bearer token, and `serve` refuses a non-loopback bind without one. TLS is left to a fronting proxy.
- Tool groups live under `pkg/mcpserver/<group>` as public packages, wired explicitly in `cmd/chatops-mcp` (no `init()` or blank-import registration).
- Each group declares honest `readOnlyHint`/`destructiveHint` annotations, even though the daemon does not trust them for granting access.

| Implemented group | Purpose |
|---|---|
| [`ping`](mcp-ping.md) | connectivity smoke test |
| [`status`](mcp-status.md) | service status pages and health endpoints |
| [`kubernetes`](mcp-k8s.md) | read-only cluster inspection (Phase 3) |
| [`host`](mcp-host.md) | reachability (resolve, ping, SSH banner) of configured hosts |

## 7. LLM and conversation

The LLM adapter exposes one request/response pair: a request with messages and tool specs, a response with either assistant text or tool calls.

- It targets OpenAI chat-completions. The first round of a turn sends `tool_choice: required`, and later rounds send `auto` (see §7.1).
- It strips a leading `<think>...</think>` block.
- It assigns `call_<index>` IDs to tool calls that lack one.
- It fails a completion on `finish_reason` of `length` or `content_filter`; a missing `finish_reason` is accepted.
- When `api_key_env` is set, it refuses every redirect.
- Optional sampling parameters are sent only when configured; otherwise the endpoint default applies.

Conversations:

- The conversation key is workspace + channel + thread timestamp.
- History is in memory, bounded by age and turn count, oldest first.
- Only earlier user messages are replayed, never assistant answers, tool calls, or tool results. Messages from other threads are never included.
- Tool execution is sequential, even when the model requests several calls at once.

### 7.1 Decline tool and forced first call

Small models often answer in plain text when they should call a tool, and chatops posts that text as the answer. Seen on 4B models:

- answering `pong` from memory instead of calling `ping`;
- writing out "I will call the ping__ping tool… The tool returns: pong" as prose;
- reporting a service healthy with no tool able to tell.

Prompt wording does not fix this: each rewording that fixes one message breaks another, such as greetings getting a chatty reply. The fix is to remove plain text as an option for the first step:

```text
round 1, tool_choice: required
    |-- MCP tool call(s) --> results --> later rounds, tool_choice: auto --> answer
    '-- decline -----------> fixed decline reply, turn ends
```

- The agent advertises one built-in tool, `decline`, alongside the MCP tools. It takes no arguments.
- Its description is as strict as the prompt, since small models weigh it as much: use it only when no available tool can help with the request.
- `decline` is handled by the agent, not an MCP server. It is the only built-in tool, so it is not a tool framework (§3).
- Its name has no `__`, so it can never collide with an MCP tool's `<server>__<tool>` name.
- A sole `decline` call ends the turn without another model call. The reply is a fixed sentence from config, never model-written text.
- A `decline` mixed with other calls is dropped, and the other calls run. The dropped call is logged, but not counted as a decline.
- Declines that end a turn appear in reply stats and metrics like any other tool step, so misrouted messages are visible.
- The sample system prompt tells the model to call `decline` for off-topic messages, instead of replying with an exact sentence.

Failure handling:

| Condition | Behavior |
|---|---|
| A forced first round returns no tool calls | protocol error: the text is never posted, the turn fails with the usual apology, and there is no retry with `auto` |
| The turn's snapshot has no MCP tools | no model call; the reply says no tools are available, not the off-topic decline |
| A configured server is down when the model declines | the decline reply names the down servers, so an outage does not read as off-topic |
| The model declines, by calling `decline` or writing the exact configured sentence, while a tool failure is unresolved (§5) | never posted as off-topic; the reply is the failure lines |
| The model declines after every failure was resolved | the normal decline |
| The model refuses in other words while a failure is unresolved | the text plus the failure lines; no fuzzy matching, since a false match would hide a real answer |

A down server's tools stay in the snapshot, so a call to one fails as a tool error the model can report.

Trade-offs:

- The guarantee is a tool call, not a grounded answer. The final answer can still claim more than the results show, such as calling a whole cluster healthy from a node list.

- The model can still pick the wrong tool, such as `decline` for `ping`. That is a visible, logged misroute rather than an invented answer.
- Some endpoints reject `required`; vLLM does unless it runs with `--tool-call-parser`. A config opt-out sends `auto` on every round, keeps `decline` advertised, and makes plain first-round text the answer again.
- A question that needs no tool, such as "what can you do?", can only be declined.

### 7.2 Model failover

`llm.models` lists one or more OpenAI-compatible endpoints. Each entry carries its own endpoint quirks (`disable_thinking`, `stream`, `sampling`), so a local vLLM and a hosted provider can back each other up.

A turn picks its first model by `llm.schedule`, then keeps it for every model call of the turn:

| Schedule | First model of a turn | Fits |
|---|---|---|
| `ordered` (default) | the first in the list | one preferred model with backups |
| `round_robin` | the next in the list, rotating per turn | equal peers sharing load |
| `sticky` | the last one that answered | avoiding a dead primary on every turn |

A model call fails over to the next model in the list, wrapping around, and the turn stays on the model that answered:

- Only errors another endpoint may not share fail over: connection errors, a model's own `timeout`, and the HTTP statuses in its `retry_http_codes`.
- `retry_http_codes` defaults to 429 and 5xx. A model may add codes specific to its endpoint, such as 401 and 403 for a key that can expire.
- Other HTTP statuses, malformed replies, and `finish_reason: length` fail the call, since they usually mean a request or prompt problem that the backup would share.
- `llm.max_model_retry` caps the models tried per call. It defaults to, and cannot exceed, the number of models, so no model is tried twice in one call.
- A model's `timeout` bounds each attempt, so a hung model leaves the rest of `agent.turn_timeout` to the next one. When the turn deadline passes, the call stops without failing over.
- The rotation is per turn, not per call, because models differ in how they call tools, and one turn planned by one model and answered by another is hard to debug.

Each model has a `name`, defaulting to its `model` and unique across the list, so two endpoints serving one model stay apart. The name identifies the model everywhere:

- Every failover logs a warning naming both models, counts in `chatops_model_failovers_total`, and names the answering model in the stats footer.
- Model call metrics observe each attempt against its own model and duration, so a failover's wait is not charged to the backup. The footer and turn stats keep the combined duration, which is what the user waited.
- A connection that drops while the reply is read fails over like one that never connects. A reply that arrives whole but fails to parse does not.

## 8. Authorization, approval, and security

Default deny. The effective tool set is the intersection of configured servers, policy rules, and the requester's roles.

- **Identity and roles:** the requester is the Slack user ID from the verified event, never message text. Roles come from a static config map of Slack user IDs to role names. Unknown or undeterminable identity fails closed.
- **Policy rules:** a rule names a server, a tool (exact or `*`), the allowed roles, and whether approval is required. Matching is on stable server/tool identity. A destructive annotation forces approval regardless of the rule.
- **Approval:** a call needing approval suspends the turn and posts Approve/Deny buttons.
    - An approval is bound to server ID, MCP tool name, SHA-256 of canonical JSON arguments, requester, conversation key, and expiry; changing any argument invalidates it.
    - On a click, the daemon verifies the clicker's identity and approver role, checks expiry and binding, rechecks policy, then resumes.
    - Self-approval is off by default. Suspended turns are lost on restart.
- **General controls:**
    - The model can never add a server, launch a command, choose an endpoint, or alter credentials; those come only from config.
    - Downstream controls (Kubernetes RBAC, Vault policies) remain enforced independently.
    - Authoritative instructions and authorization stay outside untrusted content (chat text, tool metadata, tool results).
    - Secrets are redacted from logs.
- **`chatops-mcp` over HTTP:**
    - Bearer tokens authenticate callers but do not authorize them.
    - Tool-group selection (`--tool`), downstream RBAC, and the credentials a deployment hands the process are three separate controls, each scoped on its own. ChatOps policy sits in front of all of them.
    - Unauthenticated loopback HTTP trusts every local process on the host, so privileged groups on shared hosts must use `--token-env` or stdio.

## 9. Configuration

- A single YAML file passed with `--config`, validated at startup, holding exactly one document.
- Secrets never appear in YAML. Fields ending in `_env` name the environment variable holding the secret, and stdio servers get secrets through `secret_env` rather than plaintext `env`.
- Bearer tokens and API keys are only configured with `https` URLs, or plain `http` to a loopback host.
- The annotated, runnable reference is [`../package/systemd/config.yaml`](../package/systemd/config.yaml); the README settings table is the authoritative per-field explanation.

## 10. Repository layout

```text
chatops/
├── cmd/
│   ├── chatops/               # daemon binary: main.go + kong CLI (serve)
│   └── chatops-mcp/           # built-in MCP server binary
├── internal/
│   ├── agent/                 # bounded LLM/tool turn loop
│   ├── chat/                  # chat backend interface, factory, runner; slack, discord, stdio, and unix backends
│   ├── conversation/          # per-thread serialization, history, suspended turns
│   ├── llm/                   # OpenAI-compatible adapter
│   ├── mcp/                   # SDK client wrapper, catalog, name mapping
│   ├── policy/                # roles, rules, approval binding
│   ├── config/                # YAML parsing and validation
│   ├── command/               # shared kong parser, Run, signal-aware context
│   ├── version/               # shared `version` subcommand
│   └── testutils/
├── pkg/
│   └── mcpserver/             # ping, status, kubernetes, host tool groups
├── package/                   # rpm/deb/container packaging; systemd unit
├── docs/                      # this design and the per-MCP designs
├── Makefile
├── AGENTS.md                  # CLAUDE.md symlinks here
└── README.md
```

- There is no top-level `main.go`; `main` packages live under `cmd/<binary>/`.
- Kong subcommands nest at `cmd/<binary>/<subcommand>/`, with a `Cmd` struct and `Run()` method.
- Subcommands shared by both binaries live in `internal/<name>/` (currently only `version`).
- Favor concrete types until a second implementation or a testing seam justifies an interface.

## 11. Status

- **Phase 0 — bootstrap:** done. Both binaries build and package (deb/rpm/container); main branch protection applied (required `build / build` check, signed commits, linear history, one approving review).
- **Phase 1 — local vertical slice:** done. `chatops-mcp serve` + `ping`, `internal/config`, `internal/mcp`, `internal/llm`, `internal/agent`, and the `stdio` terminal chat backend. Verified live against `http://media.xiehang.com:8080/v1` with `qwen3-1.7b-q8`.
- **Phase 2 — Slack:** done. Socket Mode, threading, retry deduplication, per-thread serialization. Chat backends are named in the config, and every one listed runs; `slack`, `discord`, `stdio`, and `unix` ship, `http`/`tcp` planned (`udp` deferred). See [`chat.md`](chat.md).
- **Phase 2.5 — service and host status:** done. The `status` and `host` groups.
- **Phase 3 — Kubernetes:** read-only slice done: 34 tools across nodes, namespaces, workloads (deployment, statefulset, daemonset, replicaset, job, pod), services, storage (pv, pvc), configmaps, and events. Mutation waits for Phase 4. See [`mcp-k8s.md`](mcp-k8s.md).
- **Phase 4 — policy and approval:** not started. Roles, rules, execution-time checks, suspended turns, Slack approval buttons; a startup warning is logged until policy exists, so until then deploy only read-only tools or fully trusted channels.
- **Chatops-reported tool failures (§5):** done.
- **Decline tool and forced first call (§7.1):** done. `agent.require_tool_call` is the opt-out, and `agent.decline_reply` the fixed reply.
- **Phase 5 — hardening:** metrics done (per-reply stats and Prometheus, see [`observability.md`](observability.md)); reconnect handling, structured audit events, and integration tests remain.

## 12. Testing strategy

- Unit tests (table-driven, testify `require`) for name mapping, argument hashing, approval binding, policy matching, conversation isolation, budget enforcement, and config validation.
- MCP contract tests using the SDK's in-memory transport against `pkg/mcpserver` groups: discovery, pagination, success/error results, list changes, timeout, reconnect, malformed responses.
- Agent tests against a deterministic fake LLM: plain reply, sequential tool calls, invalid arguments, denied call, approval suspend/resume, prompt injection in tool output, iteration exhaustion, cancellation.
- End-to-end smoke tests against a live model and cluster, run manually or behind a build tag.
- Tool-selection checks against the small models in use record the first tool chosen, not just the reply, for a fixed message set: `ping`, an operational question per group, `hello`, `thanks`, `what can you do?`, `tell me a joke`, and the mixed `ping and tell me a joke`.

## 13. Decisions

1. Slack and Discord are the production chat backends, both over outbound connections needing no public endpoint; the `stdio` terminal is for development and the `unix` socket for local access.
2. Conversation history and suspended turns are in memory only.
3. The first real integration is read-only Kubernetes via the built-in `chatops-mcp`.
4. Trust model: static Slack user ID to roles map, with per-tool rules.
5. Approval happens in Slack via buttons, by users holding the rule's approver role; self-approval is off by default.
6. Model responses are streamed by default so a timed-out turn stops generation behind proxies such as LiteLLM; chat still gets final replies in thread.
7. First deployment target is systemd using the rpm/deb packaging covering both binaries.
8. `chatops-mcp serve` runs one tool group per process (`--tool`).
9. A tool group's `--config` is a directory whose `*.yaml` files merge in file name order by one shared rule (mappings by key, lists appended, later scalars win, `null` ignored). Local settings live in their own file and never require editing a packaged one.
10. The daemon's `config.yaml` stays a single file, since a second file silently overriding LLM or credential settings is too surprising.
11. The first round of every turn forces a tool call, with a built-in `decline` tool for off-topic messages, so the model's first step is always a real tool call rather than an answer from memory or a call described in prose (§7.1).
12. Tool failures reach the user as lines chatops appends to the reply, never only through the model's wording (§5).
13. A turn stays on one model and fails over only on errors a different endpoint may not share (§7.2).

## 14. References

- MCP specification: https://modelcontextprotocol.io/specification
- Official MCP Go SDK: https://github.com/modelcontextprotocol/go-sdk
