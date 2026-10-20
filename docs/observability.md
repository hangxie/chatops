# observability

Status: implemented ([`DESIGN.md`](DESIGN.md) §11, Phase 5).

Two audiences need to know where a turn spent its time and bytes:

| Audience | Looks at | Fed by |
|---|---|---|
| The person who asked | one reply | the per-reply stats footer |
| An operator | trends across many turns | Prometheus |

Both come from one source:

- The agent records every step of a turn.
- The conversation manager records every question from receipt to delivered reply.

Neither changes how a turn runs.

## Configuration

| Setting | Default | Controls |
|---|---|---|
| `agent.show_stats` | `false` | the footer in chat |
| `metrics.listen` | unset | the Prometheus endpoint and health probes |
| (none) | always on | the per-turn log line |

- The two settings are independent, so either can be enabled without the other. Both are in the README settings table.
- The sample [`config.yaml`](../package/systemd/config.yaml) enables the endpoint on loopback (`127.0.0.1:9464`), so packaged installs get health probes and local scraping without exposing them.
- The sample shows the footer commented out, with its default.

## Steps

A turn is a sequence of steps, recorded in order with duration and outcome. A model call is named by what it returned, so a reader can tell the calls apart without counting:

| Step | Meaning |
|---|---|
| `plan` | The model returned tool calls. |
| `answer` | The model returned the final text; a successful turn ends with exactly one. |
| `model` | A model call that failed before returning either; appears only on a failed turn. |
| `tool` | One MCP tool call, named by its model-facing name. |

What each step carries:

- Model step: prompt and completion tokens from the response's `usage` block.
    - An endpoint that omits `usage` leaves tokens unreported, never zero.
    - Totals count reported tokens only.
- Tool step: the bytes the server returned, and the bytes the model saw after `max_tool_result_bytes` truncation. A truncated result often explains a poor answer.

Failures and edge cases:

- A failed turn keeps the completed steps. It also records the step that was running when it stopped, with its outcome (`timeout`, `error`).
- `Agent.Run` returns the partial stats alongside its error.
- A model or tool result that arrives after its deadline is discarded, and its step recorded as `timeout`. Dependencies may ignore cancellation.
- A tool step is timed from the model's request, so a call rejected as unknown or invalid still has a duration.
- A turn stopped by `max_iterations`, or by an identical call that failed twice in a row, ends with a plan marked `stopped`.
- A call rejected as an unknown tool or invalid arguments logs `tool call rejected` at warn level. The arguments are logged only at debug level.

## Reply footer

With `agent.show_stats: true`, the conversation manager appends the steps and totals to every reply, failures included:

    plan 2.3s (1.2k→45 tok) → kubernetes.pod_list 5.7s (123KB→16KB) → answer 6.3s (8.1k→310 tok)
    total 14.3s · 1 tool · 9.3k→355 tok · reply 2.1KB

- Tools appear as `<server>.<tool>`, using the configured server name, which is unique.
- The model-facing `<server>__<tool>` is not used, because it would render as bold in chat Markdown.
- A call to an unknown tool shows the name the model asked for.
- A model step that failed over names the model that answered, or failed last: `answer 4.0s (via openrouter, 900→20 tok)`.
- Its duration includes the failed attempts, since the user waited for them.
- The reply size is measured before sending, so it is approximate. Platform rendering (Slack mrkdwn) changes it slightly.
- The post cannot report its own latency. Measuring it would need a follow-up message or an edit, which is not worth the extra chat traffic.

The footer travels to the chat backend separately from the answer, so each backend can set it apart:

| Backend | Footer |
|---|---|
| Slack | below a divider as a context block (smaller grey text); left out of the notification text |
| Discord | subtext (`-#` lines, smaller grey text) below the answer |
| plain text | after a blank line |

## Prometheus

`metrics.listen` serves `/metrics` on its own HTTP listener.

- Collectors live in a dedicated registry wired explicitly in `serve`, never the global default registry.
- The registry also exports Go runtime and process metrics.
- The endpoint is unauthenticated, like most exporters. Bind it to loopback or a scrape-only network.
- Once configured, metrics are part of the daemon's health.
    - An unexpected server exit stops `serve` with an error, as a failed listen does at startup.
    - So a supervisor restarts the process, instead of it answering chat with no metrics.
- Labels stay low-cardinality. User, channel, conversation, and message text never become labels.

| Label | Comes from |
|---|---|
| `backend` | the configured chat backend name |
| `model` | a configured `llm.models[].name` |
| `server`, `tool` | administrator-configured MCP servers |

### Question metrics

Recorded by the conversation manager, not the agent:

- Some questions never reach the agent: they are refused as busy, or during shutdown.
- Response time must include the wait for a turn slot and reply delivery. That is what the asker experiences.

| Metric | Type | Labels |
|---|---|---|
| `chatops_questions_received_total` | counter | `backend` |
| `chatops_questions_rejected_total` | counter | `backend`, `reason` (`busy`, `closed`) |
| `chatops_response_duration_seconds` | histogram | `backend`, `outcome` (`answered`, `stopped`, `timeout`, `error`) |
| `chatops_reply_failures_total` | counter | `backend` |
| `chatops_queue_wait_seconds` | histogram | `backend` |
| `chatops_questions_pending` | gauge | |
| `chatops_turns_in_flight` | gauge | |

### Step metrics

Recorded from the agent's steps:

| Metric | Type | Labels |
|---|---|---|
| `chatops_turn_iterations` | histogram | |
| `chatops_model_call_duration_seconds` | histogram | `phase` (`plan`, `answer`, `model`), `outcome` (`ok`, `timeout`, `error`), `model` |
| `chatops_model_tokens_total` | counter | `phase`, `kind` (`prompt`, `completion`), `model` |
| `chatops_model_failovers_total` | counter | `model` (the one that failed) |
| `chatops_tool_call_duration_seconds` | histogram | `server`, `tool`, `outcome` (`ok`, `error`, `timeout`) |
| `chatops_tool_result_bytes` | histogram | `server`, `tool` |
| `chatops_tool_result_truncated_total` | counter | `server`, `tool` |
| `chatops_chat_backend_up` | gauge | `backend` |
| `chatops_mcp_server_up` | gauge | `server` |
| `chatops_mcp_tools` | gauge | `server` |
| `chatops_build_info` | gauge (always 1) | `version` |

### Notes on the metrics

- There is no separate answered or failed counter. The `_count` of `chatops_response_duration_seconds` by `outcome` counts them. `_sum`/`_count` gives the average response time.
- "Answered" means a final answer was produced and delivered, not that it was correct.
- A decline counts as answered. It shows as a `chatops.decline` tool step (server `chatops`, tool `decline`), so declines can be counted from the tool-call metrics.
- A decline dropped because the model also called other tools is only logged.
- `chatops_response_duration_seconds` observes delivered replies only. An undelivered one counts in `chatops_reply_failures_total` instead; its turn's step metrics are still recorded.
- `chatops_queue_wait_seconds` runs from receipt to taking a turn slot. So it includes waiting behind earlier messages of the same conversation.
- `chatops_model_call_duration_seconds` observes each attempt of a failed-over call against its own model:
    - a failed attempt as phase `model`, with outcome `timeout` or `error`;
    - the last attempt with the call's phase.
- So a turn's model calls can add up to more observations than `chatops_turn_iterations` counts.
- A tool the model named but no server serves is labeled `(unknown)` for both `server` and `tool`, so model output never becomes a label.
- With no reconnect yet, `chatops_mcp_server_up` stays 0 once a session ends. The daemon logs a warning when that happens.

## Health probes

The metrics listener also serves `/healthz` and `/readyz`, so probes need no second port or setting.

| Probe | Checks | Why |
|---|---|---|
| `/healthz` | nothing beyond answering HTTP | a liveness probe never restarts a daemon that is merely busy or waiting on a slow model |
| `/readyz` | startup finished, not shutting down, every chat backend connected, every MCP server up | a readiness or alerting signal |

`/readyz` failure reasons:

| Reason | When |
|---|---|
| `starting` | until the conversation manager is wired |
| `shutting down` | once it stops accepting questions |
| `chat backend <name>: not connected` | for each chat backend that is not connected |
| `mcp server <name>: down` | for each MCP server that is down |

- `/readyz` reads the same cached state as the gauges, and never calls the model or a tool. A probe that depends on the model endpoint would flap with it.
- A chat backend counts as connected only once it can receive messages (for Slack, after Socket Mode connects).
- A fresh daemon stays unready until then, and a reconnect makes it briefly unready.

### Readiness vs. activity

Readiness is independent of conversation activity. An in-flight model or tool call, or a full pending queue, does not make the daemon unready.

When the daemon checks its own status through a conversation, that turn counts in `chatops_turns_in_flight`. So `chatops_turns_in_flight >= 1` is expected during a self-check, and is not a sign of trouble.

## Logs

Each turn emits one info-level log line, with the same totals as the footer.

- It is written whether or not the footer or metrics are enabled.
- So a slow turn can be investigated after the fact.

## Out of scope

- Distributed tracing (OpenTelemetry spans across `chatops` and `chatops-mcp`); the step list covers the single-process view.
- Metrics from `chatops-mcp` itself; the daemon already measures every tool call from the caller's side.
- Per-user or per-channel usage accounting.
