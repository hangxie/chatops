# kubernetes MCP tool group

Status: implemented ([`DESIGN.md`](DESIGN.md) Phase 3). Selectable with `--tools kubernetes`.

The `kubernetes` group is ChatOps' Kubernetes integration: one tool group, organized internally by resource, exposing read-only tools. This document holds the conventions every resource follows, plus a short note per resource on what is specific to it; a resource whose design needs more than that gets its own document. All mutation is out of scope until the Phase 4 authorization and approval framework exists. Package: `pkg/mcpserver/kubernetes`.

## Resources

| Resource | Tools | Design |
|---|---|---|
| node | `node_list`, `node_status`, `node_resources` | [`mcp-k8s-node.md`](mcp-k8s-node.md) |
| namespace | `namespace_list` | [below](#namespace) |
| deployment | `deployment_list`, `deployment_pods`, `deployment_describe` | [below](#deployment) |

### namespace

`namespace_list` reports each namespace's phase and only its `True` conditions: every namespace condition describes a deletion problem (content or finalizers remaining, discovery failures), so a `True` one is exactly why a namespace is stuck `Terminating`, and the column appears only when some namespace has one. Each such condition keeps its reason and message in structured output, and its message, which often names the blocking resource kinds, follows the table as one detail line so the table stays one line per namespace. RBAC: `list` on `namespaces`, beside the group-wide `get`.

### deployment

Health builds on `kubectl rollout status`, checked in the order `Paused`, `Stalled` (`ProgressDeadlineExceeded`), `ScaledToZero`, `Progressing` (generation not observed, replicas not all updated, or more replicas than desired, which kubectl ignores during a scale-down), `Degraded` (fewer available than desired, which includes a scale-up whose new pods are not ready yet), `Healthy`. A pod belongs to the deployment only when its controller is a ReplicaSet the deployment controls, so another workload's pods carrying the same labels are excluded; its revision is `current` when that ReplicaSet is at the deployment's `deployment.kubernetes.io/revision`, `old` otherwise, and `unknown` rather than guessed when no such ReplicaSet exists yet. Deployment events are omitted, since they are mostly `ScalingReplicaSet`; the useful ones are pod events. RBAC: `get`/`list` on `deployments`, `list` on `replicasets`, `list` on `pods`.

## One group, resource-oriented internals

Kubernetes is exposed as one `kubernetes` tool group whose code is organized by resource module. Kubernetes is one external system with one client, one credential context, and one set of error conventions, so the MCP server boundary matches the integration boundary.

Tool grouping is not an authorization boundary. Three controls stay independent: tool exposure (`--tools`), ChatOps authorization (Phase 4: roles, rules, approval, enforced at execution time), and Kubernetes RBAC (enforced by the API server). Selecting a group only affects exposure, and `ReadOnlyHint` is advisory, so the read/destructive and per-requester decisions belong to the other two controls.

## Tool conventions

Tool names are resource-first and snake_case, `<resource>_<verb>`, matching the existing `service_status` tool. A resource offers the verbs that apply to it, each with a fixed meaning:

| Verb | Meaning | Arguments |
|---|---|---|
| `list` | the roster of that resource, one line per object | none, or an optional `namespace` for namespaced resources (empty lists every namespace) |
| `status` | the health of specific objects | a target list |
| `resources` | capacity, allocation, and utilization of specific objects | a target list |
| `pods` | the pods each workload object selects, with their state | a target list |
| `describe` | the full object as cleaned YAML | a target list |

Every tool is read-only (`ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: true`). Each returns a typed Go response, so `mcp.AddTool` derives the output schema and populates `StructuredContent`; the handler also sets `Content` to a concise human-readable summary rather than relying on the SDK's JSON fallback, because ChatOps targets small models that answer better from a compact text block. Text is kubectl-like: a `list` is an aligned table, and a per-target tool renders one block per target separated by a blank line.

## Targets

Every tool that reports on specific objects takes a list of targets rather than a single name, and listing is always a separate tool. A question about several objects therefore costs one list call plus one call naming all of them, so the number of model rounds does not grow with the number of objects. This matters for the small models ChatOps targets, which usually make one tool call per response and would otherwise exhaust `agent.max_iterations` on a mid-sized cluster. There is no implicit "all objects" mode: the model always names its targets, taken from the list tool.

The target list is required and non-empty, and its argument is named after the resource in the plural (`nodes`, `deployments`). Cluster-scoped targets are names; namespaced targets are objects with required `namespace` and `name` fields (`{"namespace": "argocd", "name": "argocd-server"}`), so the schema enforces both and nothing is parsed out of a combined string. Fields are trimmed, a blank one is an error, and duplicates are dropped keeping request order. A call accepts at most 50 distinct targets so the combined result stays within the agent's tool-result budget; every `describe` tool shares a lower cap of 5 because each target's output is a full object.

Targets are fetched in parallel, at most four at a time, and the output keeps request order. A target that does not exist is a normal entry with `found: false`, never a fuzzy match and never a tool error, so one bad name does not hide the others. A failure that is not a miss (unreachable cluster, RBAC-forbidden) fails the whole call, cancels the targets still in flight or queued, and reports that first failure with the target it hit. Structured output wraps the per-target entries in one list field named after the argument, e.g. `{"deployments": [...]}`, and each entry carries `found`.

A namespace that does not exist is reported as such rather than as an empty result: when a namespaced lookup comes back empty or not found, the tool checks the namespace and says `namespace "xyz" does not exist`. A `list` reports it as a tool error; a per-target tool keeps the `found: false` entry and says so in its text and a `note` field. The check runs only on an empty or missing result, so a successful call costs no extra request.

## Output conventions

Conditions preserve the three API states — `True`, `False`, and `Unknown` — and are never collapsed; a condition the object does not report is absent, distinct from an explicit `Unknown`. Age is stored as the creation timestamp in structured output and rendered to a human-readable age only in text. Quantities stay canonical in structured output and are humanized in text.

A resource with a meaningful overall state reports it as one derived health word, the same in its `list`, `pods`, and `describe` output, evaluated in a fixed order so the most actionable state wins. Each resource defines its words and rules; the raw conditions remain available in `describe`, since health is a summary, not a replacement.

A `pods` tool lists the pods the workload controls — found by its selector, then confirmed by owner reference, since a selector match alone is not ownership — through a shared pod summary whose columns follow `kubectl get pods`, native sidecars included. Where the resource has rollout revisions, each pod is marked `current`, `old`, or `unknown`, so a stuck rollout shows which generation is failing; the resource's note defines how its current revision is found.

A `describe` tool returns the object as YAML, because YAML is what models read most reliably and it never drifts from the API. A one-line header names the object and its health. Server bookkeeping is dropped: `metadata.managedFields`, the `kubectl.kubernetes.io/last-applied-configuration` annotation (often the largest field), `metadata.resourceVersion`, `metadata.uid`, and `metadata.generateName`. Literal environment values in a pod template are redacted, because describe output is relayed into chat:

    env:
    - name: DB_PASSWORD
      value: <redacted …x7Q>
    - name: LOG_LEVEL
      value: <redacted>
    - name: API_TOKEN
      valueFrom:
        secretKeyRef: {name: app-secret, key: token}

Every non-empty literal `env[].value` in containers and init containers is redacted. A value of at least 12 characters keeps its last 3 characters, enough to tell which credential is deployed without revealing it; a shorter value is fully redacted, since 3 characters of a short secret is too much of it. The last characters are kept rather than the first because credentials often start with a fixed prefix (`sk-`, `ghp_`) that identifies nothing. `valueFrom` references name a Secret or ConfigMap key, not its value, and are shown unchanged. Redaction covers `env` only: a secret passed in `command` or `args` cannot be recognized reliably and is not redacted, so such workloads should move it to `env` or a Secret.

## Cluster connection and credentials

Connection resolution (standard client-go), in order: an explicit `--kubeconfig` is honored exactly and never silently falls back (a missing or invalid file fails startup); otherwise in-cluster configuration is used when running as a pod; otherwise a local kubeconfig (`$KUBECONFIG`, else `~/.kube/config`) is the development fallback. An optional `--kube-context` selects a context and explicitly requests kubeconfig-based resolution, taking precedence over in-cluster detection so it is never silently ignored in-cluster. These flags live on `chatops-mcp serve`, gated so they are only valid with `--tools kubernetes` (the same pattern as `--status-config`).

When the `rest.Config` is built, two client-level defaults are set: a finite request timeout as a backstop to the per-call context deadline, and a `chatops-mcp/<version>` user agent so requests are identifiable in the API server's audit logs. Every API client is built from this one config. The HTTP bearer token (`--token-env`) only authenticates the MCP client to `chatops-mcp`; it says nothing about what the Kubernetes credentials may do.

Every tool is read-only, so RBAC reduces to `get`/`list` grants. Each resource lists the grants its tools need, and the README ships a sample `ClusterRole` combining them; the group itself needs `get` on `namespaces` for the missing-namespace check. Reads granted cluster-wide expose that resource in every namespace, and redaction in tool output does not change what the credentials can read: the grant is the real boundary and should be scoped deliberately. A grant more powerful than the data it serves (such as `nodes/proxy`) is kept out of the baseline, and granting it is the only switch for the feature that needs it, so no server flag can disagree with the RBAC.

## Package layout

One Go package per resource under `pkg/mcpserver/kubernetes/` (`node/`, `namespace/`, `deployment/`), each exporting `Register(server, kube.Clients)` and holding its types, tools, and text rendering, with a one-to-one `_test.go` per file. The root package only resolves the connection and calls each resource's `Register`, explicitly. Code every resource shares lives in `internal/kube` (clients, target validation and bounded parallel fetch, the generic per-target handler, the missing-namespace check, the pod summary, describe cleaning and redaction, table and age helpers), and fixtures shared by more than one resource's tests live in `internal/kubetest`; `internal/` keeps both private to the group. Wiring in `cmd/chatops-mcp/serve/serve.go` adds `"kubernetes"` to the `groups` map and the `--kubeconfig`/`--kube-context` flags.

## Adding a resource

1. Add a row to the Resources table and a short note covering only what the conventions here do not: health rules, revision matching, RBAC, and any deliberate omission. Write a separate `docs/mcp-k8s-<resource>.md` only when the design needs more than that.
2. Add a `<resource>/` package exporting `Register`, with its tests, built on the shared code in `internal/kube`.
3. Call `<resource>.Register` from the root `Register`.
4. Add the resource's grants to the README `ClusterRole` and its tools to the README tool table.

A change to a convention in this document is a design change of its own, not part of adding a resource.

## Error handling

Local configuration errors fail `chatops-mcp serve` fast, consistent with how `status` validates its config at load: a bad kubeconfig path, a malformed kubeconfig, or an unknown `--kube-context`. Everything network-dependent is a runtime tool error the model can relay, because a cluster can become unreachable at any time: cluster unreachable or TLS failure, API-call timeout, and RBAC-forbidden (naming the verb and resource, e.g. `get nodes`, so the operator can fix the role). A missing object is a normal result, not an error. Error messages never echo bearer tokens or raw kubeconfig contents. There is no startup connectivity probe gate, because it would say nothing about whether a later call succeeds.

## Testing

Shared code is table-tested once: target validation (empty, blank, duplicate, over each cap, order, namespaced fields), the parallel fetch (bounded concurrency, error naming the target, cancellation of in-flight and queued targets after a failure), the missing-namespace check, pod status precedence, YAML cleaning, and redaction at its length boundary with `valueFrom` untouched. Connection resolution is tested for the explicit-kubeconfig, context, in-cluster (via an injectable seam), and development-fallback branches.

Each resource's tools are tested with client-go's fake clientset and table-driven fixtures, asserting both text and structured output, and each per-target tool is called with a mix of existing and missing targets. Because the fake clientset does not reproduce API-server RBAC or network behavior, failure paths (RBAC-forbidden, timeout, transport failure) are tested with explicitly injected fake reactions.

A live acceptance runs the tools against a real cluster and confirms the output; it never runs in CI, is not a unit test, hardcodes no cluster, uses whatever kubeconfig/context the operator points it at, and the target is optional and never assumed to be production.

## Future work

Large clusters will eventually need `list` output capped or paginated so a big roster does not overflow the model's context; the tools assume a homelab-sized cluster and return every object. Pod-level tools (`pod_status`, `pod_resources`, `pod_logs`) and events are a later diagnostics slice. Mutating operations (cordon, drain, restart, scale, delete) are deferred until Phase 4 provides per-requester authorization, Slack approval, and durable audit; they will be ordinary tools in the same group, marked non-read-only and gated by policy, not by group membership.

## Decisions

1. Kubernetes is one MCP tool group (`kubernetes`), organized internally by resource; the MCP server boundary matches the integration boundary.
2. Tool names are `<resource>_<verb>` in snake_case, with a fixed meaning per verb.
3. Every tool is read-only; no mutation until Phase 4.
4. Tool grouping is not an authorization boundary; exposure, ChatOps authorization, and Kubernetes RBAC are three independent controls.
5. Per-object tools take a required list of targets (at most 50, or 5 for every `describe` tool; deduplicated; fetched four at a time; output in request order) and listing is a separate tool; there is no implicit all-objects mode.
6. Namespaced targets are `{namespace, name}` objects.
7. A missing target is a `found: false` entry, not a call failure; a missing namespace is named as such.
8. Conditions preserve `True`/`False`/`Unknown` and are never collapsed; an absent condition is absent, not a manufactured `Unknown`.
9. Age is stored as a creation timestamp in structured output and rendered to a human-readable age only in text.
10. A resource with an overall state reports one derived health word, consistent across its tools.
11. `pods` tools list only pods the workload controls (selector, then owner reference) with kubectl-style status, marking rollout revision where the resource has one.
12. `describe` tools return cleaned YAML with literal environment values redacted, keeping the last 3 characters only for values of at least 12; `command`/`args` are not redacted.
13. Connection: explicit `--kubeconfig` never silently falls back, in-cluster config when running as a pod, local kubeconfig as the development fallback; an explicit `--kube-context` forces kubeconfig resolution.
14. The `rest.Config` sets a finite request timeout and a `chatops-mcp/<version>` user agent.
15. Network failures are runtime tool errors; only local config problems fail startup.
16. RBAC is the only switch for features that need a powerful grant; such grants stay out of the baseline.
