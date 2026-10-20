# kubernetes MCP tool group

Status: implemented ([`DESIGN.md`](DESIGN.md) Phase 3). Selectable with `--tool kubernetes`. Package: `pkg/mcpserver/kubernetes`.

The `kubernetes` group is ChatOps' Kubernetes integration:

- one tool group, organized internally by resource;
- read-only tools only. All mutation is out of scope until the Phase 4 authorization and approval framework exists.

This document holds:

- the conventions every resource follows, and
- a short note per resource on what is specific to it.

A resource whose design needs more than a note gets its own document.

## Resources

| Resource | Tools | Design |
|---|---|---|
| node | `node_list`, `node_status`, `node_resources` | [`mcp-k8s-node.md`](mcp-k8s-node.md) |
| namespace | `namespace_list` | [below](#namespace) |
| deployment | `deployment_list`, `deployment_pods`, `deployment_describe` | [below](#deployment) |
| pod | `pod_list`, `pod_describe`, `pod_log`, `pod_resources` | [below](#pod) |
| statefulset | `statefulset_list`, `statefulset_pods`, `statefulset_describe` | [below](#statefulset) |
| daemonset | `daemonset_list`, `daemonset_pods`, `daemonset_describe` | [below](#daemonset) |
| replicaset | `replicaset_list`, `replicaset_pods`, `replicaset_describe` | [below](#replicaset) |
| service | `service_list`, `service_pods`, `service_describe` | [below](#service) |
| job | `job_list`, `job_pods`, `job_log`, `job_describe` | [below](#job) |
| pv | `pv_list`, `pv_describe` | [below](#pv) |
| pvc | `pvc_list`, `pvc_describe` | [below](#pvc) |
| configmap | `configmap_list`, `configmap_describe` | [below](#configmap) |
| event | `event_list` | [below](#event) |

### Health words at a glance

Each health word list is checked in the order shown; the first match wins.

| Resource | Health, in check order |
|---|---|
| deployment | `Paused`, `Stalled`, `ScaledToZero`, `Progressing`, `Degraded`, `Healthy` |
| statefulset | `ScaledToZero`, `Progressing`, `Degraded`, `Healthy` |
| daemonset | `Progressing` (generation), `NoNodes`, `Progressing` (rollout), `Degraded`, `Healthy` |
| replicaset | `ScaledToZero`, `Progressing`, `Degraded`, `Healthy` |
| service | `External`, `NoEndpoints`, `Unavailable`, `Degraded`, `Healthy` |
| job | `Failed`, `Complete`, `Suspended`, `Running`, `Pending` |
| pod | kubectl-style pod status |
| pv | its phase: `Available`, `Bound`, `Released`, `Failed`, `Pending` |
| pvc | its phase: `Bound`, `Pending`, `Lost` |
| node | its `Ready` condition |

### namespace

- `namespace_list` reports each namespace's phase and only its `True` conditions.
- Every namespace condition describes a deletion problem (content or finalizers remaining, discovery failures), so a `True` one is exactly why a namespace is stuck `Terminating`.
- The conditions column appears only when some namespace has one.
- Each condition keeps its reason and message in structured output.
- In text, the message follows the table as one detail line, so the table stays one line per namespace. It often names the blocking resource kinds.
- RBAC: `list` on `namespaces`, beside the group-wide `get`.

### deployment

Health builds on `kubectl rollout status`:

| Health | When |
|---|---|
| `Paused` | the deployment is paused |
| `Stalled` | `ProgressDeadlineExceeded` |
| `ScaledToZero` | desired is 0 |
| `Progressing` | generation not observed, replicas not all updated, or more replicas than desired (which kubectl ignores during a scale-down) |
| `Degraded` | fewer available than desired, including a scale-up whose new pods are not ready yet |
| `Healthy` | otherwise |

Pods and revisions:

- A pod belongs to the deployment only when its controller is a ReplicaSet the deployment controls, so another workload's pods carrying the same labels are excluded.
- A pod's revision is `current` when that ReplicaSet is at the deployment's `deployment.kubernetes.io/revision`, `old` otherwise, and `unknown` (not guessed) when no such ReplicaSet exists yet.

Deployment events are omitted: they are mostly `ScalingReplicaSet`, and the useful ones are pod events.

RBAC: `get`/`list` on `deployments`, `list` on `replicasets` (widened to `get` by the replicaset tools), `list` on `pods`.

### pod

Health is the kubectl-style pod status.

| Tool | Notes |
|---|---|
| `pod_list` | Filters by namespace and by node (a server-side field selector); names each pod's controller. |
| `pod_log` | See below. |
| `pod_resources` | Requests and limits per container, native sidecars included. Usage from the metrics API is measured against the limit, else the request. Degrades to a `usage_note` like `node_resources`. |

`pod_log`:

- Returns every main container unless one is named. Init and ephemeral containers can be named.
- Tails 50 lines by default, at most 500.
- Streams each container's output, and keeps its newest 16 KB, cut at a line boundary.
- There is no server-side byte limit: it would cut the newest output instead of the oldest.
- The API's 400 answers (no previous instance, container not started) become a per-container note carrying the API's message.
- `previous` shows the crashed instance behind a `CrashLoopBackOff`.
- Logs are raw container output relayed into chat and cannot be redacted, so the `pods/log` grant is the boundary.

RBAC: `get`/`list` on `pods`, `get` on `pods/log`, and `get` on `pods` in `metrics.k8s.io`.

### statefulset

Health follows `kubectl rollout status` for statefulsets:

| Health | When |
|---|---|
| `ScaledToZero` | desired is 0 |
| `Progressing` | generation not observed, a rolling update not finished, or more replicas than desired |
| `Degraded` | fewer available than desired |
| `Healthy` | otherwise |

- A partitioned rolling update is finished once every pod at or above the partition is updated.
- An `OnDelete` update is never in progress, since it waits for pods to be deleted; its pods still show as `old`.
- A pod belongs to the statefulset when the statefulset is its controller.
- A pod's revision is its `controller-revision-hash` label compared with `status.updateRevision`.

RBAC: `get`/`list` on `statefulsets`.

### daemonset

Counts are against the nodes that should run a pod (`desiredNumberScheduled`). Health follows `kubectl rollout status` for daemonsets:

| Health | When |
|---|---|
| `Progressing` | generation not observed |
| `NoNodes` | no node matches (rather than a scale to zero) |
| `Progressing` | a rolling update not finished; an `OnDelete` update never is |
| `Degraded` | fewer available than desired |
| `Healthy` | otherwise |

A daemonset's status does not name its current revision. So:

1. take the newest ControllerRevision the daemonset controls;
2. compare its `controller-revision-hash` label with each owned pod's label.

RBAC: `get`/`list` on `daemonsets` and `list` on `controllerrevisions`.

### replicaset

A replicaset is one revision with no rollout of its own, so its pods carry no revision.

| Health | When |
|---|---|
| `ScaledToZero` | desired and current both 0 |
| `Progressing` | generation not observed, or current not yet desired (scaling either way) |
| `Degraded` | fewer available than desired |
| `Healthy` | otherwise |

The list follows `kubectl get rs` (desired, current, ready) and names each replicaset's controller, usually a deployment whose old revisions show as `ScaledToZero`.

RBAC: `get`/`list` on `replicasets`.

### service

A service's pods are the ones its EndpointSlices name, not whatever its selector matches.

- EndpointSlices are where traffic actually goes.
- This also covers services without a selector.

| Health | When |
|---|---|
| `External` | an ExternalName service |
| `NoEndpoints` | no endpoints |
| `Unavailable` | none ready |
| `Degraded` | some not ready |
| `Healthy` | otherwise |

Merging endpoints:

- A backend listed in several slices (a dual-stack service repeats a pod once per address family) is merged into one endpoint with all its addresses.
- When the entries disagree, the merge is conservative, so the result never depends on slice order. An endpoint is ready and serving only if every entry says so, and terminating if any does.
- Terminating endpoints are left out of the counts but still listed.
- An unset `ready` condition means ready, as the API defines.
- `service_list` counts endpoints with one EndpointSlice list for the whole scope.

RBAC: `get`/`list` on `services` and `list` on `endpointslices` in `discovery.k8s.io`.

### job

A job runs to an end rather than rolling out. A finished job keeps its last counts, so health reads the terminal conditions first:

| Health | When |
|---|---|
| `Failed` | a `Failed` or `FailureTarget` condition |
| `Complete` | a `Complete` condition |
| `Suspended` | `spec.suspend` is set |
| `Running` | active pods |
| `Pending` | otherwise |

- The list shows succeeded over required completions, failed pods, duration (start to finish, or to now while running), and the controlling cronjob.
- A pod belongs to the job when the job is its controller. Each pod is one attempt, so there is no revision.
- `job_log` reads the job's 3 newest pods, newest first, with `pod_log`'s options and limits, and says how many attempts were left out.

RBAC: `get`/`list` on `jobs` in `batch`, plus the pod log grant.

### pv

- Persistent volumes are cluster-scoped, so `pv_describe` takes plain names in `pvs`, and a missing one is just `found: false`.
- Health is the volume's phase.
- The list follows `kubectl get pv`, with kubectl's short access modes and the bound claim as namespace/name.
- A volume's events come from `event_list`, not a volume tool.

RBAC: `get`/`list` on `persistentvolumes`.

### pvc

- Health is the claim's phase.
- The list follows `kubectl get pvc`, showing the capacity the bound volume provides rather than the request. A pending resize therefore shows the old size until it finishes; its condition is in `pvc_describe`.

RBAC: `get`/`list` on `persistentvolumeclaims`.

### configmap

- Configmaps hold configuration, not secrets, so values are shown rather than redacted.
- A value over 2 KB keeps its first 2 KB, cut at a character boundary and marked with its full size, so one embedded dashboard cannot crowd out the rest.
- Binary data shows its size instead of base64.
- A configmap has no health, so its describe header names only the object.
- Some workloads put credentials in configmaps anyway; the `configmaps` grant is the boundary for those.

RBAC: `get`/`list` on `configmaps`.

### event

Events are one API about every kind of object, so there is one `event_list` rather than a per-resource events tool. It also covers resources added later without new tools.

| Question | Arguments |
|---|---|
| a resource's events (a pod, a persistent volume, ...) | `kind` and `name` |
| a namespace's events | `namespace` |
| the cluster's warnings | `type: Warning` |

- Filters are server-side event field selectors.
- The API matches `kind` exactly, but models often copy the lowercase kinds kubectl prints.
    - Common lowercase and short names (`pod`, `pvc`, `sts`) are mapped to the API kind, and filtered server-side.
    - Any other kind is matched case-insensitively after listing.
- Rows are newest first by kubectl's last-seen time, and the cap keeps the newest, not the first in API order.
- Cluster-scoped objects record their events in `default`, so filtering those by namespace finds nothing.
- The cluster keeps events for about an hour.

RBAC: `list` on `events`.

## One group, resource-oriented internals

Kubernetes is exposed as one `kubernetes` tool group, whose code is organized by resource module.

- Kubernetes is one external system, with one client, one credential context, and one set of error conventions.
- So the MCP server boundary matches the integration boundary.

Tool grouping is not an authorization boundary. Three controls stay independent:

| Control | Enforced by | Decides |
|---|---|---|
| Tool exposure | `--tool` | which group is served |
| ChatOps authorization (Phase 4) | roles, rules, approval, at execution time | who may call what |
| Kubernetes RBAC | the API server | what the credentials can read or change |

Selecting a group only affects exposure, and `ReadOnlyHint` is advisory. So read/destructive and per-requester decisions belong to the other two controls.

## Tool conventions

Tool names are resource-first snake_case, `<resource>_<verb>`, matching the existing `service_status` tool. A resource offers the verbs that apply to it. Each verb has a fixed meaning:

| Verb | Meaning | Arguments |
|---|---|---|
| `list` | the roster of that resource, one line per object | none, or an optional `namespace` for namespaced resources (empty lists every namespace) |
| `status` | the health of specific objects | a target list |
| `resources` | capacity, allocation, and utilization of specific objects | a target list |
| `pods` | the pods each workload object selects, with their state | a target list |
| `describe` | the full object as cleaned YAML | a target list |
| `log` | recent container output | a target list, plus container, tail, and previous-instance options |

Every tool:

- is read-only (`ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: true`);
- returns a typed Go response, so `mcp.AddTool` derives the output schema and populates `StructuredContent`;
- sets `Content` to a concise human-readable summary, rather than the SDK's JSON fallback, because small models answer better from a compact text block;
- renders kubectl-like text: a `list` is an aligned table, and a per-target tool renders one block per target separated by a blank line.

### Result cap

Every `list` tool, and each target of a `pods` tool, returns at most `max_results` rows (default 50, set in `--config`). So a big roster cannot overflow the model's context.

- Structured output carries `total`, the count before the cap.
- The text ends with `(showing the first N of M pods)`, so the model narrows the call instead of reading a partial roster as complete.
- Rows past the cap are dropped in API order (namespace, then name). There is no pagination.

The cap is applied after the full API list, not with the API's `limit`, because a server-side limit:

- returns the first rows in storage order, which is wrong for `event_list` (it keeps the newest);
- would undercount after client-side filtering (owner references, case-insensitive kinds);
- has no exact total when a selector is used.

The cap bounds only what reaches the model.

- Each call still lists, filters, and summarizes every match, so API response size and server memory grow with the cluster.
- That is fine at homelab scale. A large cluster would need server-side pagination.

## Targets

Every tool that reports on specific objects takes a list of targets, not a single name. Listing is always a separate tool.

- A question about several objects costs one list call plus one call naming all of them, so model rounds do not grow with the number of objects.
- This matters for small models, which usually make one tool call per response and would otherwise exhaust `agent.max_iterations` on a mid-sized cluster.
- There is no implicit "all objects" mode: the model always names its targets, taken from the list tool.

### Target list shape

| Rule | Detail |
|---|---|
| Argument name | the resource in the plural (`nodes`, `deployments`); required and non-empty |
| Cluster-scoped target | a name |
| Namespaced target | an object with required `namespace` and `name`: `{"namespace": "argocd", "name": "argocd-server"}`. The schema enforces both, and nothing is parsed out of a combined string |
| Cleanup | fields trimmed; a blank one is an error; duplicates dropped, keeping request order |
| Cap | 50 distinct targets, so the combined result stays within the tool-result budget |
| Cap for `describe` and `log` | 5, because each target's output is a full object or raw output |

### Fetching and errors

- Targets are fetched in parallel, at most four at a time, and the output keeps request order.
- A target that does not exist is a normal entry with `found: false`. It is never a fuzzy match, and never a tool error, so one bad name does not hide the others.
- A failure that is not a miss (unreachable cluster, RBAC-forbidden):
    - fails the whole call;
    - cancels the targets still in flight or queued;
    - reports the first failure, with the target it hit.
- Structured output wraps the entries in one list field named after the argument, e.g. `{"deployments": [...]}`, and each entry carries `found`.

### Missing namespaces

A namespace that does not exist is reported as such, not as an empty result. When a namespaced lookup comes back empty or not found, the tool checks the namespace. If it is missing, the tool says `namespace "xyz" does not exist`:

| Tool kind | Reports it as |
|---|---|
| `list` | a tool error |
| per-target | the `found: false` entry, plus the text and a `note` field |

The check runs only on an empty or missing result, so a successful call costs no extra request.

## Output conventions

| Field | Structured output | Text |
|---|---|---|
| Conditions | `True`, `False`, `Unknown`, never collapsed; an unreported condition is absent, not `Unknown` | same |
| Age | the creation timestamp | a human-readable age |
| Quantities | canonical | humanized |

### Health

A resource with a meaningful overall state reports it as one derived health word.

- The word is the same in its `list`, `pods`, and `describe` output.
- It is evaluated in a fixed order, so the most actionable state wins.
- Each resource defines its words and rules.
- The raw conditions remain in `describe`, since health is a summary, not a replacement.

### `pods` tools

- A `pods` tool lists the pods the workload controls. They are found by its selector, then confirmed by owner reference, since a selector match alone is not ownership.
- Rows use a shared pod summary whose columns follow `kubectl get pods`, native sidecars included.
- Where the resource has rollout revisions, each pod is marked `current`, `old`, or `unknown`. So a stuck rollout shows which generation is failing.
- The resource's note defines how its current revision is found.

### `describe` tools

A `describe` tool returns the object as YAML, with a one-line header naming the object and its health. Why YAML: models read it most reliably, and it never drifts from the API.

Server bookkeeping is dropped:

- `metadata.managedFields`
- the `kubectl.kubernetes.io/last-applied-configuration` annotation (often the largest field)
- `metadata.resourceVersion`, `metadata.uid`, `metadata.generateName`

Literal environment values in a pod template are redacted, because describe output is relayed into chat:

    env:
    - name: DB_PASSWORD
      value: <redacted …x7Q>
    - name: LOG_LEVEL
      value: <redacted>
    - name: API_TOKEN
      valueFrom:
        secretKeyRef: {name: app-secret, key: token}

| Value | Shown as |
|---|---|
| literal `env[].value`, 12 characters or more | `<redacted …xyz>`, keeping the last 3, enough to tell which credential is deployed |
| literal `env[].value`, shorter | `<redacted>`; 3 characters of a short secret is too much of it |
| `valueFrom` reference | unchanged; it names a Secret or ConfigMap key, not its value |
| `command`, `args` | unchanged; secrets there cannot be recognized reliably, so move them to `env` or a Secret |

- Redaction covers containers and init containers.
- The last characters are kept, not the first, because credentials often start with a fixed prefix (`sk-`, `ghp_`) that identifies nothing.

## Cluster connection and credentials

Every group setting lives in its `--config` directory, shipped as `package/systemd/mcp-kubernetes.d/`. It is the same pattern as `status`, so new settings add a field rather than a flag.

| Field | Purpose |
|---|---|
| `kubeconfig` | kubeconfig file, honored exactly; a relative path resolves against the config directory |
| `context` | kubeconfig context; forces kubeconfig resolution |
| `max_results` | row cap for `list` and `pods` tools (see [Result cap](#result-cap)) |

- The directory is optional, because every field has a working default. Unknown fields are errors.
- A file of your own beside the shipped `00-default.yaml` overrides single fields; a later file wins. See the README's [Configuration directories](../README.md#configuration-directories).
- A relative `kubeconfig` resolves against the config directory, not the working directory, so it means the same thing wherever the server starts.

### Connection resolution

Standard client-go, first match wins:

1. An explicit `kubeconfig`, honored exactly. A missing or invalid file fails startup; it never silently falls back.
2. An explicit `context`. It requests kubeconfig-based resolution, and takes precedence over in-cluster detection, so it is never silently ignored in a pod.
3. In-cluster configuration, when running as a pod.
4. A local kubeconfig (`$KUBECONFIG`, else `~/.kube/config`), the development fallback.

The `rest.Config` sets two client-level defaults, and every API client is built from it:

- a finite request timeout, as a backstop to the per-call context deadline;
- a `chatops-mcp/<version>` user agent, so requests are identifiable in the API server's audit logs.

The HTTP bearer token (`--token-env`) only authenticates the MCP client to `chatops-mcp`. It says nothing about what the Kubernetes credentials may do.

### RBAC

- Every tool is read-only, so RBAC reduces to `get`/`list` grants.
- Each resource lists the grants its tools need, and the README ships a sample `ClusterRole` combining them.
- The group itself needs `get` on `namespaces` for the missing-namespace check.
- Reads granted cluster-wide expose that resource in every namespace.
- Redaction in tool output does not change what the credentials can read. The grant is the real boundary, so scope it deliberately.
- A grant more powerful than the data it serves (such as `nodes/proxy`) stays out of the baseline.
- Granting it is the only switch for the feature that needs it, so no server flag can disagree with the RBAC.

## Package layout

| Path | Holds |
|---|---|
| `pkg/mcpserver/kubernetes` | the root package: resolves the connection, loads `--config`, and calls each resource's `Register` explicitly |
| `pkg/mcpserver/kubernetes/<resource>/` | one package per resource (`node/`, `namespace/`, `deployment/`, ...), exporting `Register(server, kube.Clients)` with its types, tools, and text rendering |
| `.../kubernetes/internal/kube` | code every resource shares (below) |
| `.../kubernetes/internal/kubetest` | fixtures shared by more than one resource's tests |
| `cmd/chatops-mcp/serve/serve.go` | adds `"kubernetes"` to the `groups` map |

`internal/kube` holds:

- clients;
- target validation and bounded parallel fetch;
- the generic per-target handler;
- the namespaced lookup and missing-namespace check;
- the workload list row, and the owned-pods lookup with revision marking;
- the pod summary;
- log fetching;
- describe cleaning and redaction;
- table, age, and quantity helpers.

`internal/` keeps both shared packages private to the group. Every file has a one-to-one `_test.go`.

## Adding a resource

1. Add a row to the Resources table, and a short note.
    - Cover only what the conventions here do not: health rules, revision matching, RBAC, and any deliberate omission.
    - Write a separate `docs/mcp-k8s-<resource>.md` only when the design needs more.
2. Add a `<resource>/` package exporting `Register`, with its tests, built on the shared code in `internal/kube`.
3. Call `<resource>.Register` from the root `Register`.
4. Add the resource's grants to the README `ClusterRole` and its tools to the README tool table.

A change to a convention in this document is a design change of its own, not part of adding a resource.

## Error handling

| Kind | Examples | Result |
|---|---|---|
| Local configuration | missing `--config` directory, malformed file or unknown field in it, bad kubeconfig path, malformed kubeconfig, unknown `context` | `chatops-mcp serve` fails fast at startup, as `status` does |
| Network-dependent | cluster unreachable, TLS failure, API-call timeout, expired or rejected credentials, RBAC-forbidden | a runtime tool error the model can relay |
| Missing object | | a normal result, not an error |

- RBAC-forbidden errors name the verb and resource (e.g. `get nodes`), so the operator can fix the role.
- Error messages never echo bearer tokens, authorization headers, or raw kubeconfig contents. chatops posts tool errors to chat verbatim, so this is the only guard.
- There is no startup connectivity probe. A cluster can become unreachable at any time, so it would say nothing about whether a later call succeeds.
- Runtime errors are the client's own, passed through unchanged. A categorizing layer would only reword them:
    - they already say what failed: `Unauthorized`, `nodes is forbidden: …`, `dial tcp …: connection refused`, `x509: …`;
    - chatops prefixes them with the tool name.

## Testing

Shared code is table-tested once:

- target validation: empty, blank, duplicate, over each cap, order, namespaced fields;
- the parallel fetch: bounded concurrency, error naming the target, cancellation of in-flight and queued targets after a failure;
- the missing-namespace check, pod status precedence, YAML cleaning;
- redaction at its length boundary, with `valueFrom` untouched;
- connection resolution: explicit kubeconfig, context, in-cluster (via an injectable seam), and development fallback.

Each resource's tools:

- are tested with client-go's fake clientset and table-driven fixtures, asserting both text and structured output;
- call each per-target tool with a mix of existing and missing targets;
- test failure paths (RBAC-forbidden, timeout, transport failure) with explicitly injected fake reactions, since the fake clientset does not reproduce API-server RBAC or network behavior.

A live acceptance run checks the tools against a real cluster.

- It is not a unit test, and never runs in CI.
- It hardcodes no cluster. It uses whatever kubeconfig and context the operator points it at.
- It never assumes the target is production.

## Future work

Mutating operations (cordon, drain, restart, scale, delete) wait for Phase 4 to provide per-requester authorization, Slack approval, and durable audit.

- They will be ordinary tools in the same group, marked non-read-only.
- They will be gated by policy, not by group membership.

## Decisions

1. Kubernetes is one MCP tool group (`kubernetes`), organized internally by resource; the MCP server boundary matches the integration boundary.
2. Tool names are `<resource>_<verb>` in snake_case, with a fixed meaning per verb.
3. Every tool is read-only; no mutation until Phase 4.
4. Tool grouping is not an authorization boundary; exposure, ChatOps authorization, and Kubernetes RBAC are three independent controls.
5. Per-object tools take a required list of targets: at most 50 (5 for `describe` and `log`), deduplicated, fetched four at a time, output in request order. Listing is a separate tool; there is no implicit all-objects mode.
6. Namespaced targets are `{namespace, name}` objects.
7. A missing target is a `found: false` entry, not a call failure; a missing namespace is named as such.
8. Conditions preserve `True`/`False`/`Unknown` and are never collapsed; an absent condition is absent, not a manufactured `Unknown`.
9. Age is stored as a creation timestamp in structured output and rendered as a human-readable age only in text.
10. A resource with an overall state reports one derived health word, consistent across its tools.
11. `pods` tools list only pods the workload controls (selector, then owner reference) with kubectl-style status, marking rollout revision where the resource has one.
12. `describe` tools return cleaned YAML with literal environment values redacted, keeping the last 3 characters only for values of at least 12; `command`/`args` are not redacted.
13. Connection follows [Connection resolution](#connection-resolution): an explicit `kubeconfig` never silently falls back, and an explicit `context` forces kubeconfig resolution. Every group setting lives in the optional `--config` directory.
14. The `rest.Config` sets a finite request timeout and a `chatops-mcp/<version>` user agent.
15. Network failures are runtime tool errors; only local config problems fail startup.
16. RBAC is the only switch for features that need a powerful grant; such grants stay out of the baseline.
17. `list` tools and each `pods` target return at most `max_results` rows (default 50), capped after listing, with the total reported; no pagination.
