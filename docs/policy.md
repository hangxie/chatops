# Authorization and Approval

Status: accepted design, not implemented yet. This is Phase 4 of [`DESIGN.md`](DESIGN.md) §11.

ChatOps decides who may use which tool, and which calls wait for a human answer before they run. The model never decides either.

## Prior art

Each part follows a model already proven elsewhere, rather than a new one:

| Part | Follows | What is borrowed |
|---|---|---|
| Roles and grants | Kubernetes RBAC | principals bound to roles; grants only allow, never deny, and add up across roles; anything not granted is denied |
| Gates | GitHub Actions environment protection rules | approval rules attach to the tool, not to the person, so they apply to everyone allowed to call it |
| Self-approval off | GitHub "prevent self-review", Teleport access requests | the requester cannot be their own reviewer |
| Pending requests | Teleport access requests | each request has an ID, reviewer roles, and an expiry |
| Requester confirms, approver approves | the two-person rule | the person asking commits to the exact action, and a second person allows it |
| Running exactly what was approved | Terraform saved plans | the approved calls run verbatim, and are checked again just before they run |

## Principals

A principal is `<backend name>:<User>`, where `User` is the sender identity the chat backend verified ([`chat.md`](chat.md#conversation-key-versus-sender-identity)).

| Backend | Example principal |
|---|---|
| `slack` | `eng-slack:U0123ABCD` |
| `discord` | `ops-discord:123456789012345678` |
| `stdio` | `console:1001`, the numeric user ID running the process |
| `unix` | `ops-sock:1001`, the peer's numeric user ID |

- `<backend>:*` matches every sender on that backend.
- The backend name, not the platform, is the prefix. Two Slack workspaces never share principals.
- An empty `User` matches nothing, not even `<backend>:*`.
- Message text and conversation keys are never identity.

### Local users

`stdio` and `unix` follow roles, grants and gates like every other backend. Being able to reach the terminal or connect to the socket grants nothing on its own.

Several SREs can use the socket at once, each on their own connection. The peer's user ID names each of them:

- It comes from the peer credentials, `SO_PEERCRED` on Linux and `getpeereid` on macOS, read once when the connection is accepted.
- The numeric ID is used as is, with no lookup of a user name. That keeps it stable and free of name-service failures.
- The kernel supplies it, so a client cannot fake it.

## Trust boundary

chatops can do no more than the credentials it holds: the kubeconfig, tokens and keys in its config and environment.

| Deployment | Who controls the config | What policy protects |
|---|---|---|
| shared daemon, under a service account | the operators | the service account's credentials, from every chat user and socket user |
| a personal instance, run by one person | that person | nothing; they can edit or remove it |

- Policy is a security boundary only for a shared daemon. In a personal instance it is a guard against your own mistakes.
- Give a personal instance your own credentials, never a service account's. Downstream controls, such as Kubernetes RBAC on your kubeconfig, then keep it to what you could do yourself.
- A shared daemon could act as the requester downstream instead, such as Kubernetes impersonation. That is tracked in [#81](https://github.com/hangxie/chatops/issues/81).

## Configuration

```yaml
policy:
  roles:
    viewer: ["eng-slack:*", "ops-sock:*"]
    oncall: ["eng-slack:U0123ABCD", "ops-discord:123456789012345678"]
    lead: ["eng-slack:U0456EFGH"]
  grants:
    - roles: [viewer, oncall]
      tools: ["status/*", "host/*", "kubernetes/node_list", "kubernetes/pod_list"]
    - roles: [oncall]
      tools: ["kubernetes/deployment_restart"]
  gates:
    - tools: ["kubernetes/deployment_restart"]
      mode: approve
      approvers: [lead]
  approval:
    ttl: 10m
    self_approve: false
    max_pending: 3
    max_pending_total: 100
```

- No `policy` section means no tool is allowed. Every deployment must grant tools explicitly.
- Startup fails on any of these:
    - a role referenced but not defined;
    - a grant without `roles` or `tools`;
    - a gate without `tools`, or with a `mode` other than `confirm` or `approve`;
    - an `approve` gate without `approvers`, or a `confirm` gate with them.

Tool patterns, used by grants and gates:

| Pattern | Matches |
|---|---|
| `<server>/<tool>` | one tool, by configured server ID and MCP tool name |
| `<server>/*` | every tool of one server, including tools it adds later |
| `*` | every tool |

Matching uses the stable server/tool identity, never the model-facing name.

## Grants

A grant lets roles call tools. Like Kubernetes RBAC:

- Grants only allow. There is no deny rule.
- A principal may call a tool if any of their roles is granted it by any grant.
- A tool no grant gives the principal is denied.

A wildcard grant covers tools a server adds later, write tools included. Grant write tools by exact name, as `deployment_restart` is above.

Two checks use the grants:

1. When a turn starts, the tool list sent to the model holds only the tools the requester may call. Gated tools are listed, so the model can propose them.
2. Before every call, the grant is checked again. A denial goes back to the model as a tool error.

## Gates

A gate makes calls to its tools wait for a human answer. It applies to everyone allowed to call the tool, whichever grant allowed it.

| `mode` | Needs before the call runs | Answer words |
|---|---|---|
| `confirm` | the requester's `yes` | `yes` / `no` |
| `approve` | the requester's `yes`, and an approver's `approve`, in either order | requester: `yes` / `no`; approver: `approve` / `deny` |

- `approve` includes the requester's confirmation. The model, not the requester, wrote the calls, so the requester commits to the exact calls before they run, and a second person allows them.
- A tool with no gate runs without waiting.
- Several gates matching one tool all apply. The strictest mode wins, and an approver must hold an approver role from every matching `approve` gate.
- Server annotations can only make this stricter. A tool that is not `readOnlyHint` and is `destructiveHint` (the MCP default) needs at least `confirm`, even without a gate.
- `approve` and `confirm` use different words, so a casual "yes" never approves anything.

Self-approval:

- `self_approve: false` (default) stops a requester approving their own request, even when they hold an approver role.
- With `self_approve: true`, a requester holding an approver role answers `approve`, which counts as both their `yes` and the approval.

The approver must be able to see the request:

- Each backend reports whether a conversation is shared: a Slack or Discord channel or thread is, but a DM, `stdio`, and a `unix` connection are not.
- A batch needing `approve` in a conversation that is not shared is refused when proposed, unless the requester may approve it themselves. Nothing runs, and the reply says to ask in a shared channel.
- Approving from another conversation may come later as its own feature.

## Suspended turns

The gate sits in the agent loop, before each batch of tool calls the model proposes:

```text
model proposes a batch
        |
        v
  any call gated? --no--> preflight, run the batch, continue the loop
        |
       yes
        v
  suspend the whole batch, post the prompt, end the turn
        |
  answers arrive
        |
   all needed answers -----> preflight, run the stored batch verbatim, continue the loop
   no / deny / cancel -----> reply "Cancelled." or "Denied by <principal>."
```

- The whole batch waits, even if only one call is gated. It needs every answer any of its calls needs, so a mixed `confirm` and `approve` batch needs the requester's `yes` and an approval.
- On approval, the stored calls run exactly as stored. The model is not asked again.
- The resumed loop gates every new batch again. An approval covers only the batch it was given for.

### Preflight

Before any call of a batch runs, every call in it is checked. If any check fails, none of them runs:

| Check | Against |
|---|---|
| the requester may call the tool | the current roles and grants |
| the tool exists | the current tool catalog |
| the arguments are valid | the tool's current schema |
| every needed answer was given | the current gates |
| each answer came from someone allowed to give it | the current roles and `self_approve` |

- A suspended batch can wait up to `approval.ttl`, so it is checked again when it resumes, not only when it was proposed.
- A failed preflight on a resumed batch ends the turn, naming the failed check. A failed preflight on a new batch from the model goes back to it as tool errors, as today.
- Preflight cannot make the calls themselves all-or-nothing, but it stops a batch failing halfway for a reason known in advance.

### Running a batch

- Calls run one at a time, in the order the model gave them ([`DESIGN.md`](DESIGN.md) §7), never in parallel.
- After a failed write, the remaining calls of the batch do not run.
- The reply lists which calls ran, which one failed, and which did not run.

## Write calls are never retried

A write is a call to any tool that is not `readOnlyHint`. If a write fails, times out, or is cut off, its outcome is unknown. Calling it again could repeat a change, so:

- The turn ends at the first failed write. The model is not asked to continue, so it cannot call it again.
- chatops never re-runs it: no automatic retry, no reuse of its approval, no re-run of the message after a restart.
- The reply names the call, its arguments, and what is known, such as "timed out after 30s, outcome unknown". It asks the requester to check the current state and decide what to do next.

This is stricter than the read-only rule in [`DESIGN.md`](DESIGN.md) §5, which stops a turn only after an identical call has failed twice.

## The prompt

The prompt names its request ID, each call's server, tool, and canonical JSON arguments, who must answer, and when the request expires:

```text
[r7kq] Run kubernetes/deployment_restart {"namespace":"web","targets":["api"]}?
Needs "yes r7kq" from the requester and "approve r7kq" from role lead.
Expires in 10 minutes (14:32 UTC).
```

- The expiry is shown on every backend, both relative and absolute.
- Slack and Discord show the absolute time in each reader's own time zone (Slack `<!date^…>`, Discord `<t:…>`).

## Where the prompt and the answer go

The prompt goes to the thread the request came from. If there is none, it goes as close to the request as the platform allows. A text answer counts only if it arrives in the same conversation as the prompt.

| Backend | Prompt | A text answer counts when |
|---|---|---|
| `slack` | in the request's thread, even with `reply_mode: channel` | posted in that thread, mentioning the bot as usual |
| `discord`, thread mode | in the request's thread | posted in that thread |
| `discord`, channel mode or DM | a Discord reply to the request | sent as a Discord reply to the prompt |
| `stdio`, `unix` | the next output line | it is a later line of the same connection |

Why Slack always uses a thread and Discord uses replies:

- Every top-level message in a channel, and in a DM, is its own conversation. A top-level "yes" could not be matched to its prompt.
- Slack has no reply-to-message apart from threads, so the prompt always goes to a thread.
- Discord marks a reply with the message it answers, so the backend maps a reply to the prompt onto the prompt's conversation.

Buttons avoid the problem entirely, since a click names its prompt.

## Buttons

Slack and Discord show the prompt with buttons. Other backends use the text answers above, and text answers also keep working on Slack and Discord.

| | Slack | Discord |
|---|---|---|
| Widget | Block Kit buttons | message component buttons |
| Click delivery | Socket Mode interactive events | Gateway `INTERACTION_CREATE` |
| Public endpoint | not needed | not needed |
| Setup | turn on Interactivity in the Slack app | none |

- `confirm` shows **Yes** / **No**. `approve` shows **Yes** / **No** for the requester and **Approve** / **Deny** for approvers.
- A click counts exactly as a typed answer from the clicker's verified identity. It goes through the same checks: role, self-approval, expiry, and taking the pending batch out of the conversation.
- A click from someone who may not answer gets a message only they can see (Slack ephemeral, Discord ephemeral interaction response). The prompt stays.
- Once answered, the prompt is edited to show the outcome and who answered, and the buttons are removed. A click after expiry edits it to "expired".
- A form dialog (Slack modal, Discord modal) can ask for a reason on **Deny**. That is optional and comes after the buttons.

## Pending requests

A suspended batch is a pending request, held by the conversation manager next to history. A conversation may hold several, from different requesters.

### Request IDs

- Every pending request gets a random ID of four characters from lowercase letters and digits, without look-alikes (`0`, `o`, `1`, `l`), such as `r7kq`. That gives about a million IDs.
- An ID is never reused within a conversation's lifetime, expired requests included. A late `yes r7kq` can never reach a newer request.
- An ID only routes an answer, and never replaces the checks on who may answer.

### Answers

| Who | Words |
|---|---|
| the requester | `yes` / `no`, or `cancel` |
| an approver | `approve` / `deny` |

- An answer is the whole message: the word, optionally followed by the ID (`yes r7kq`, `yes [r7kq]`). It is trimmed, case-insensitive, with trailing punctuation removed.
- Any other message is a normal turn and leaves every pending request as it is. So a requester can check something first and then answer.

An answer is matched to a request by the first rule that applies:

| # | Answer | Matches |
|---|---|---|
| 1 | a button click | the prompt it was shown with |
| 2 | a Discord reply to a prompt | that prompt |
| 3 | a word with an ID | that request, if the sender may answer it |
| 4 | a bare word | the only live request in the conversation the sender may answer |

- Rules 1 and 2 are bound to one prompt message, and rule 3 to one ID. A thread only scopes a conversation, so it never picks between several requests in it.
- When a bare word fits several requests, nothing runs. The reply lists them with their IDs and asks which one.
- When an answer fits none, the reply says so (see [State and deployment](#state-and-deployment)), whether the request expired or was lost in a restart.

On a terminal (`stdio`) or socket (`unix`) session, and in a Slack or Discord thread, there is usually one request at a time, so a bare `yes` just works.

### Limits

Too many open requests are hard to follow, so their number is capped:

| Setting | Default | Bounds |
|---|---|---|
| `approval.max_pending` | 3 | live requests per conversation, 1 to 10 |
| `approval.max_pending_total` | 100 | live requests across all conversations |
| — | 1 | live requests per requester per conversation |

- A requester's new gated batch replaces their older request in the same conversation, and the prompt says so ("replaces [r7kq]"). The replacement is one step under the manager lock, and is allowed at a limit, since it does not add a request.
- Any other new gated batch at a limit is refused, and nothing runs. The reply lists the waiting IDs, so someone can answer or cancel one first.
- A full conversation never drops an older request to make room. That would cancel someone else's request without telling them.

### State changes

- The request is taken out of the conversation under the manager lock before it runs. A duplicate event or a double click cannot run it twice.
- An expired request no longer counts toward the limits. It is kept only to answer late replies, until the conversation's history expires.
- Pending requests are in memory only and lost on restart.

History keeps only the original request:

- stored: the request that proposed the batch;
- not stored: the prompt, the answer words, and the tool transcript.

The transcript lives only in the pending batch.

## Policy changes

- Roles, grants and gates are read from the config at startup. There is no reload.
- A change takes effect on restart, which also drops every pending request. No request can outlive the policy it was proposed under.
- Preflight still checks the current policy on every resume. Policy held in shared storage ([#80](https://github.com/hangxie/chatops/issues/80)) could change while a request waits, and preflight is what stops a revoked role from running a stored batch.

## State and deployment

Pending requests are runtime state, like conversation history. They live in memory until shared storage lands ([#80](https://github.com/hangxie/chatops/issues/80)).

chatops runs as a single instance:

- Each chat backend holds one connection per bot.
- So every event, and every answer to a prompt, reaches the one process holding the pending request.

A restart loses every pending request. Users then get a useful reply instead of a confusing one:

- An answer that matches no request is not passed to the model as a normal turn.
- It gets a fixed reply: "I have no pending request r7kq. It may have expired, or chatops restarted. Ask again."
- Recognizing an answer needs only the message's shape (an answer word, optionally an ID), not the lost state.

## Time limits

- `approval.ttl` (default 10 minutes) bounds how long answers are accepted. Every needed answer must arrive within it. Nothing is posted when it lapses.
- A resumed turn gets a fresh `agent.turn_timeout` and the usual per-tool timeout. Waiting for answers does not count.

## Audit

Each decision is a structured log line with the principal, conversation, request ID, server, tool, and SHA-256 of the canonical arguments:

| Event | When |
|---|---|
| `policy denied` | a call fails the call-time check |
| `approval requested` | a batch is suspended |
| `approval answered` | an answer arrives, naming who answered and with which word |
| `approval granted` / `approval refused` | the batch has every answer it needs, or one answer said no |
| `approval expired` | an answer arrives after `ttl` |
| `approval cancelled` / `approval replaced` | the requester cancels, or sends a new gated batch |
| `approval refused: limit` / `approval refused: not shared` | a gated batch hits a limit, or needs an approver who cannot see the conversation |
| `preflight failed` | a batch fails preflight, naming the check |

## Delivery

| Step | Scope |
|---|---|
| 1 | this design |
| 2 | roles and grants: filtered tool list and call-time check |
| 3 | gates, preflight, and suspended turns with text answers |
| 4 | Slack and Discord buttons over the same pending batch; optional deny-reason modal |
| 5 | first mutating tool: `kubernetes` `deployment_restart` |

## Decisions

1. Principals are `<backend>:<User>`, so backends never share identities.
2. Default deny: no policy section, no tools.
3. Grants follow Kubernetes RBAC: allow-only, additive across roles, no deny rules.
4. Gates attach to tools, like GitHub environment protection rules, and apply to everyone allowed to call them. Several matching gates all apply.
5. `confirm` needs the requester's `yes`. `approve` needs that and an approver's `approve`, so the requester always commits to the exact calls.
6. A batch is approved as a whole, preflighted in full, and runs verbatim and in order; approval never extends to later batches.
7. Text answers first, so approval works on every backend; buttons come later, on Slack and Discord only.
8. The prompt stays in the request's thread, or for Discord outside threads, is a reply to the request. Every prompt shows its expiry.
9. Request IDs are four random characters, never reused within a conversation. A bare answer only counts when it fits exactly one request, so an ambiguous answer never runs anything.
10. At most 3 live requests per conversation by default and one per requester. A requester's new request replaces their old one, even at a limit; any other new request at a limit is refused.
11. A batch needing an approver is refused in a conversation the approver cannot see, such as a DM.
12. A failed write ends the turn and is never retried; the requester decides what to do next.
13. `stdio` and `unix` users are named by their numeric user ID and follow policy like everyone else; reaching the terminal or socket grants nothing.
14. chatops can do no more than its credentials allow. Policy protects a shared daemon's credentials; a personal instance gets its owner's credentials.
15. Policy is read at startup only, and every resume checks the current policy again.
