# observability

Status: implemented ([`DESIGN.md`](DESIGN.md) §11, Phase 5).

Two consumers need to know where a turn spent its time and bytes: the person who asked, looking at one reply, and an operator, looking at trends across many turns. Both are fed from one source: the agent records every step of a turn, and the conversation manager records every question from receipt to delivered reply. A per-reply stats footer renders one turn; Prometheus aggregates all of them. Neither changes how a turn runs.

## Configuration

The footer and the metrics endpoint are two independent settings, both off by default, so either can be enabled without the other: `agent.show_stats` controls the footer in chat and `metrics.listen` starts the Prometheus endpoint. The per-turn log line is always on. Both settings appear commented out, with their defaults, in the sample [`config.yaml`](../package/systemd/config.yaml) and in the README settings table.

## Steps

A turn is a sequence of steps, recorded in order with duration and outcome. A model call is named by what it returned, so a reader can tell the calls apart without counting iterations:

- **plan:** the model returned tool calls.
- **answer:** the model returned the final text; a successful turn ends with exactly one.
- **model:** a model call that failed before returning either; it appears only on a failed turn.
- **tool:** one MCP tool call, named by its model-facing name.

A model step carries prompt and completion tokens from the response's `usage` block; an endpoint that omits `usage` leaves tokens unreported, never zero, and totals count reported tokens only. A tool step carries two sizes: the bytes the server returned and the bytes the model saw after `max_tool_result_bytes` truncation, because a truncated result often explains a poor answer.

A failed turn keeps the steps that completed and records the step that was running when it stopped, with its outcome (`timeout`, `error`); `Agent.Run` returns the partial stats alongside its error. A model or tool result that arrives after its deadline is discarded and its step recorded as `timeout`, because dependencies may ignore cancellation. A tool step is timed from the model's request, so a call rejected as unknown or invalid still has a duration. A turn stopped by `max_iterations`, or by an identical call that failed twice in a row, ends with a plan marked `stopped`. A call rejected as an unknown tool or invalid arguments logs `tool call rejected` at warn level without the arguments, which are logged only at debug level.

## Reply footer

With `agent.show_stats: true` (default `false`), the conversation manager appends the steps and totals to the end of every reply, failures included:

    plan 2.3s (1.2k→45 tok) → kubernetes.pod_list 5.7s (123KB→16KB) → answer 6.3s (8.1k→310 tok)
    total 14.3s · 1 tool · 9.3k→355 tok · reply 2.1KB

Tools appear as `<server>.<tool>`, using the configured server name, which is unique across servers (the sample config names each built-in group's server after the group); the model-facing `<server>__<tool>` would render as bold in chat markdown. A call to an unknown tool shows the name the model asked for. The reply size is measured before sending and is approximate: platform rendering (Slack mrkdwn) changes it slightly, and the post itself cannot report its own latency. Measuring exactly would need a follow-up message or an edit, which is not worth the extra chat traffic.

The footer travels to the chat backend separately from the answer, so each backend can set it apart: Slack shows it below a divider as a context block (smaller grey text), and its notification text omits it; plain-text backends print it after a blank line.

## Prometheus

`metrics.listen` in the config (unset by default) serves `/metrics` on its own HTTP listener. Collectors live in a dedicated registry wired explicitly in `serve`, never the global default registry, which also exports Go runtime and process metrics. The endpoint is unauthenticated, like most exporters; bind it to loopback or a scrape-only network. Once configured, metrics are part of the daemon's health: an unexpected server exit stops `serve` with an error, as a failed listen does at startup, so a supervisor restarts the whole process instead of it answering chat with no metrics.

Labels stay low-cardinality: `backend` is the configured chat backend name, `server` and `tool` come from administrator-configured MCP servers. User, channel, conversation, and message text never become labels.

Question metrics are recorded by the conversation manager, because some questions never reach the agent (refused as busy or during shutdown), and because response time must include the wait for a turn slot and reply delivery, which is what the asker experiences.

| Metric | Type | Labels |
|---|---|---|
| `chatops_questions_received_total` | counter | `backend` |
| `chatops_questions_rejected_total` | counter | `backend`, `reason` (`busy`, `closed`) |
| `chatops_response_duration_seconds` | histogram | `backend`, `outcome` (`answered`, `stopped`, `timeout`, `error`) |
| `chatops_reply_failures_total` | counter | `backend` |
| `chatops_queue_wait_seconds` | histogram | `backend` |
| `chatops_questions_pending` | gauge | |
| `chatops_turns_in_flight` | gauge | |

Step metrics are recorded from the agent's steps:

| Metric | Type | Labels |
|---|---|---|
| `chatops_turn_iterations` | histogram | |
| `chatops_model_call_duration_seconds` | histogram | `phase` (`plan`, `answer`, `model`), `outcome` (`ok`, `timeout`, `error`) |
| `chatops_model_tokens_total` | counter | `phase`, `kind` (`prompt`, `completion`) |
| `chatops_tool_call_duration_seconds` | histogram | `server`, `tool`, `outcome` (`ok`, `error`, `timeout`) |
| `chatops_tool_result_bytes` | histogram | `server`, `tool` |
| `chatops_tool_result_truncated_total` | counter | `server`, `tool` |
| `chatops_chat_backend_up` | gauge | `backend` |
| `chatops_mcp_server_up` | gauge | `server` |
| `chatops_mcp_tools` | gauge | `server` |
| `chatops_build_info` | gauge (always 1) | `version` |

There is no separate answered or failed counter: the `_count` series of `chatops_response_duration_seconds` by `outcome` already counts them, and its `_sum`/`_count` gives the average response time. "Answered" means a final answer was produced and delivered, not that it was correct; a scope-gate decline counts as answered because its text is indistinguishable from help without a structured signal from the model.

`chatops_response_duration_seconds` observes delivered replies only; an undelivered one counts in `chatops_reply_failures_total` instead, while its turn's step metrics are still recorded. `chatops_queue_wait_seconds` runs from receipt to taking a turn slot, so it includes waiting behind earlier messages of the same conversation. A tool the model named but no server serves is labeled `(unknown)` for both `server` and `tool`, so model output never becomes a label. With no reconnect yet, `chatops_mcp_server_up` stays 0 once a session ends; the daemon logs a warning when that happens.

## Health probes

The metrics listener also serves `/healthz` and `/readyz`, so probes need no second port or setting. `/healthz` checks nothing beyond answering HTTP, so a liveness probe never restarts a daemon that is merely busy or waiting on a slow model. `/readyz` reports `starting` until the conversation manager is wired, `shutting down` once it stops accepting questions, each chat backend that is not connected, and each MCP server that is down; it reads the same cached state as the gauges and never calls the model or a tool, because a probe that depends on the model endpoint would flap with it. A chat backend counts as connected only once it can receive messages (for Slack, after Socket Mode connects), so a fresh daemon stays unready until then, and a Slack reconnect makes it briefly unready.

### Readiness vs. activity

Readiness is independent of conversation activity: an in-flight model call or MCP tool call, or a full pending queue, does not make the daemon unready. When the daemon checks its own status through a conversation, that conversation's turn is counted in `chatops_turns_in_flight`, so `chatops_turns_in_flight >= 1` is expected during a self-check and does not indicate an unhealthy daemon. `/readyz` answers 503 only when a readiness dependency fails: startup has not finished, shutdown is in progress, a chat backend is not connected, or a configured MCP server is down.

## Logs

Each turn also emits one info-level log line with the same totals as the footer, whether or not the footer or metrics are enabled, so a slow turn can be investigated after the fact.

## Out of scope

- Distributed tracing (OpenTelemetry spans across `chatops` and `chatops-mcp`); the step list covers the single-process view.
- Metrics from `chatops-mcp` itself; the daemon already measures every tool call from the caller's side.
- Per-user or per-channel usage accounting.
