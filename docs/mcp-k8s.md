# kubernetes MCP tool group

Status: implemented ([`DESIGN.md`](DESIGN.md) Phase 3). Selectable with `--tools kubernetes`.

The `kubernetes` group is ChatOps' Kubernetes integration: one tool group, organized internally by resource, exposing read-only tools. This document holds the conventions every resource follows, plus a short note per resource on what is specific to it; a resource whose design needs more than that gets its own document. All mutation is out of scope until the Phase 4 authorization and approval framework exists. Package: `pkg/mcpserver/kubernetes`.

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

### namespace

`namespace_list` reports each namespace's phase and only its `True` conditions: every namespace condition describes a deletion problem (content or finalizers remaining, discovery failures), so a `True` one is exactly why a namespace is stuck `Terminating`, and the column appears only when some namespace has one. Each such condition keeps its reason and message in structured output, and its message, which often names the blocking resource kinds, follows the table as one detail line so the table stays one line per namespace. RBAC: `list` on `namespaces`, beside the group-wide `get`.

### deployment

Health builds on `kubectl rollout status`, checked in the order `Paused`, `Stalled` (`ProgressDeadlineExceeded`), `ScaledToZero`, `Progressing` (generation not observed, replicas not all updated, or more replicas than desired, which kubectl ignores during a scale-down), `Degraded` (fewer available than desired, which includes a scale-up whose new pods are not ready yet), `Healthy`. A pod belongs to the deployment only when its controller is a ReplicaSet the deployment controls, so another workload's pods carrying the same labels are excluded; its revision is `current` when that ReplicaSet is at the deployment's `deployment.kubernetes.io/revision`, `old` otherwise, and `unknown` rather than guessed when no such ReplicaSet exists yet. Deployment events are omitted, since they are mostly `ScalingReplicaSet`; the useful ones are pod events. RBAC: `get`/`list` on `deployments`, `list` on `replicasets` (widened to `get` by the replicaset tools), `list` on `pods`.

### pod

Health is the kubectl-style pod status. `pod_list` filters by namespace and by node (a server-side field selector) and names each pod's controller. `pod_log` returns every main container unless one is named (init and ephemeral containers can be named), tails 50 lines by default and at most 500, streams each container's output and keeps its newest 16 KB cut at a line boundary (no server-side byte limit, which would cut the newest output instead of the oldest), and turns the API's 400 answers (no previous instance, container not started) into a per-container note carrying the API's message; `previous` shows the crashed instance behind a `CrashLoopBackOff`. Logs are raw container output relayed into chat and cannot be redacted, so the `pods/log` grant is the boundary. `pod_resources` reports requests and limits per container, native sidecars included, with usage from the metrics API measured against the limit, else the request, and degrades to a `usage_note` like `node_resources`. RBAC: `get`/`list` on `pods`, `get` on `pods/log`, and `get` on `pods` in `metrics.k8s.io`.

### statefulset

Health follows `kubectl rollout status` for statefulsets, checked in the order `ScaledToZero`, `Progressing` (generation not observed, a rolling update not finished, or more replicas than desired), `Degraded` (fewer available than desired), `Healthy`. A partitioned rolling update is finished once every pod at or above the partition is updated, and an `OnDelete` update is never in progress, since it waits for pods to be deleted; its pods still show as `old`. A pod belongs to the statefulset when the statefulset is its controller, and its revision is its `controller-revision-hash` label compared with `status.updateRevision`. RBAC: `get`/`list` on `statefulsets`.

### daemonset

Counts are against the nodes that should run a pod (`desiredNumberScheduled`). Health follows `kubectl rollout status` for daemonsets, checked in the order `Progressing` (generation not observed), `NoNodes` (no node matches, rather than a scale to zero), `Progressing` (a rolling update not finished; an `OnDelete` update never is), `Degraded` (fewer available than desired), `Healthy`. A daemonset's status does not name its current revision, so it is the `controller-revision-hash` label of the newest ControllerRevision the daemonset controls, compared with each owned pod's label. RBAC: `get`/`list` on `daemonsets` and `list` on `controllerrevisions`.

### replicaset

A replicaset is one revision with no rollout of its own, so health is `ScaledToZero` (desired and current both 0), `Progressing` (generation not observed or current not yet desired, scaling either way), `Degraded` (fewer available than desired), or `Healthy`, and its pods carry no revision. The list follows `kubectl get rs` (desired, current, ready) and names each replicaset's controller, usually a deployment whose old revisions show as `ScaledToZero`. RBAC: `get`/`list` on `replicasets`.

### service

A service's pods are the ones its EndpointSlices name, which is where traffic actually goes, not whatever its selector matches; this also covers services without a selector. A backend listed in several slices (a dual-stack service repeats a pod in one slice per address family) is merged into one endpoint with all its addresses, conservatively when the entries disagree: ready and serving only if every entry says so, terminating if any does, so the result never depends on slice order; terminating ones are left out of the counts but still listed, and an unset `ready` condition means ready, as the API defines. Health is `External` for an ExternalName service, otherwise `NoEndpoints`, `Unavailable` (none ready), `Degraded` (some not ready), or `Healthy`; `service_list` counts endpoints with one EndpointSlice list for the whole scope. RBAC: `get`/`list` on `services` and `list` on `endpointslices` in `discovery.k8s.io`.

### job

A job runs to an end rather than rolling out, so its health reads the terminal conditions first, since a finished job keeps its last counts: `Failed` (a `Failed` or `FailureTarget` condition), `Complete`, `Suspended`, `Running` (active pods), else `Pending`. The list shows succeeded over required completions, failed pods, duration (start to finish, or to now while running), and the controlling cronjob. A pod belongs to the job when the job is its controller; each pod is one attempt, so there is no revision. `job_log` reads the job's 3 newest pods, newest first, with `pod_log`'s options and limits, and says how many attempts were left out. RBAC: `get`/`list` on `jobs` in `batch`, plus the pod log grant.

### pv

Persistent volumes are cluster-scoped, so `pv_describe` takes plain names in `pvs` and a missing one is just `found: false`. Health is the volume's phase (`Available`, `Bound`, `Released`, `Failed`, `Pending`); the list follows `kubectl get pv`, with kubectl's short access modes and the bound claim as namespace/name. A volume's events come from `event_list` rather than a volume tool. RBAC: `get`/`list` on `persistentvolumes`.

### pvc

Health is the claim's phase (`Bound`, `Pending`, `Lost`); the list follows `kubectl get pvc`, with the capacity the bound volume provides rather than the request, so a pending resize shows the old size until it finishes (its condition is in `pvc_describe`). RBAC: `get`/`list` on `persistentvolumeclaims`.

### configmap

Configmaps hold configuration, not secrets, so values are shown rather than redacted; any value over 2 KB keeps its first 2 KB, cut at a character boundary and marked with its full size, so one embedded dashboard cannot crowd out the rest, and binary data shows its size instead of base64. A configmap has no health, so its describe header names only the object. Some workloads put credentials in configmaps anyway, and the `configmaps` grant is the boundary for those. RBAC: `get`/`list` on `configmaps`.

### event

Events are one API about every kind of object, so there is one `event_list` rather than a per-resource events tool: it serves a resource's events (`kind` and `name`, such as a pod or a persistent volume), a namespace's (`namespace`), and the cluster's warnings (`type: Warning`), and covers resources added later without new tools. Filters are server-side event field selectors; `kind` is matched exactly by the API, so common lowercase and short names (`pod`, `pvc`, `sts`) are mapped to the API kind and filtered server-side, while any other kind is matched without regard to case after listing, since models often copy the lowercase kinds kubectl prints. Rows are newest first by kubectl's last-seen time and the cap keeps the newest, rather than the first in API order. Cluster-scoped objects record their events in `default`, so filtering those by namespace finds nothing; and the cluster keeps events for about an hour. RBAC: `list` on `events`.

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
| `log` | recent container output | a target list, plus container, tail, and previous-instance options |

Every tool is read-only (`ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: true`). Each returns a typed Go response, so `mcp.AddTool` derives the output schema and populates `StructuredContent`; the handler also sets `Content` to a concise human-readable summary rather than relying on the SDK's JSON fallback, because ChatOps targets small models that answer better from a compact text block. Text is kubectl-like: a `list` is an aligned table, and a per-target tool renders one block per target separated by a blank line.

Every `list` tool, and each target of a `pods` tool, returns at most `max_results` rows (default 50, set in `--kubernetes-config`), so a big roster cannot overflow the model's context. The cap is applied after the full API list, not with the API's `limit`, because a server-side limit returns the first rows in storage order (wrong for `event_list`, which keeps the newest), would undercount after client-side filtering (owner references, case-insensitive kinds), and has no exact total when a selector is used. Structured output carries `total`, the count before the cap, and the text ends with `(showing the first N of M pods)`, so the model narrows the call instead of reading a partial roster as complete. Rows past the cap are dropped in API order (namespace, then name); there is no pagination. The cap bounds only what reaches the model: each call still lists, filters, and summarizes every match, so API response size and server memory grow with the cluster; that is acceptable at homelab scale, and a large cluster would need server-side pagination instead.

## Targets

Every tool that reports on specific objects takes a list of targets rather than a single name, and listing is always a separate tool. A question about several objects therefore costs one list call plus one call naming all of them, so the number of model rounds does not grow with the number of objects. This matters for the small models ChatOps targets, which usually make one tool call per response and would otherwise exhaust `agent.max_iterations` on a mid-sized cluster. There is no implicit "all objects" mode: the model always names its targets, taken from the list tool.

The target list is required and non-empty, and its argument is named after the resource in the plural (`nodes`, `deployments`). Cluster-scoped targets are names; namespaced targets are objects with required `namespace` and `name` fields (`{"namespace": "argocd", "name": "argocd-server"}`), so the schema enforces both and nothing is parsed out of a combined string. Fields are trimmed, a blank one is an error, and duplicates are dropped keeping request order. A call accepts at most 50 distinct targets so the combined result stays within the agent's tool-result budget; every `describe` and `log` tool shares a lower cap of 5 because each target's output is a full object or raw output.

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

Every setting of the group lives in one YAML file, `--kubernetes-config` (shipped as `package/systemd/mcp-kubernetes.yaml`), the same pattern as `--status-config`, so new settings add a field rather than a flag. The file is optional because every field has a working default; unknown fields are errors, and the flag is only valid with `--tools kubernetes`. Its `kubeconfig` and `context` fields drive connection resolution, and `max_results` caps result rows (see Output conventions).

Connection resolution (standard client-go), in order: an explicit `kubeconfig` is honored exactly and never silently falls back (a missing or invalid file fails startup); otherwise in-cluster configuration is used when running as a pod; otherwise a local kubeconfig (`$KUBECONFIG`, else `~/.kube/config`) is the development fallback. An optional `context` selects a context and explicitly requests kubeconfig-based resolution, taking precedence over in-cluster detection so it is never silently ignored in-cluster. A relative `kubeconfig` is resolved against the config file's directory, not the working directory, so the file means the same thing wherever the server starts.

When the `rest.Config` is built, two client-level defaults are set: a finite request timeout as a backstop to the per-call context deadline, and a `chatops-mcp/<version>` user agent so requests are identifiable in the API server's audit logs. Every API client is built from this one config. The HTTP bearer token (`--token-env`) only authenticates the MCP client to `chatops-mcp`; it says nothing about what the Kubernetes credentials may do.

Every tool is read-only, so RBAC reduces to `get`/`list` grants. Each resource lists the grants its tools need, and the README ships a sample `ClusterRole` combining them; the group itself needs `get` on `namespaces` for the missing-namespace check. Reads granted cluster-wide expose that resource in every namespace, and redaction in tool output does not change what the credentials can read: the grant is the real boundary and should be scoped deliberately. A grant more powerful than the data it serves (such as `nodes/proxy`) is kept out of the baseline, and granting it is the only switch for the feature that needs it, so no server flag can disagree with the RBAC.

## Package layout

One Go package per resource under `pkg/mcpserver/kubernetes/` (`node/`, `namespace/`, `deployment/`), each exporting `Register(server, kube.Clients)` and holding its types, tools, and text rendering, with a one-to-one `_test.go` per file. The root package only resolves the connection and calls each resource's `Register`, explicitly. Code every resource shares lives in `internal/kube` (clients, target validation and bounded parallel fetch, the generic per-target handler, the namespaced lookup and missing-namespace check, the workload list row and owned-pods lookup with revision marking, the pod summary, log fetching, describe cleaning and redaction, table, age, and quantity helpers), and fixtures shared by more than one resource's tests live in `internal/kubetest`; `internal/` keeps both private to the group. Wiring in `cmd/chatops-mcp/serve/serve.go` adds `"kubernetes"` to the `groups` map and the `--kubernetes-config` flag, loaded by the root package's `Load`.

## Adding a resource

1. Add a row to the Resources table and a short note covering only what the conventions here do not: health rules, revision matching, RBAC, and any deliberate omission. Write a separate `docs/mcp-k8s-<resource>.md` only when the design needs more than that.
2. Add a `<resource>/` package exporting `Register`, with its tests, built on the shared code in `internal/kube`.
3. Call `<resource>.Register` from the root `Register`.
4. Add the resource's grants to the README `ClusterRole` and its tools to the README tool table.

A change to a convention in this document is a design change of its own, not part of adding a resource.

## Error handling

Local configuration errors fail `chatops-mcp serve` fast, consistent with how `status` validates its config at load: a missing or malformed `--kubernetes-config`, an unknown field in it, a bad kubeconfig path, a malformed kubeconfig, or an unknown `context`. Everything network-dependent is a runtime tool error the model can relay, because a cluster can become unreachable at any time: cluster unreachable or TLS failure, API-call timeout, and RBAC-forbidden (naming the verb and resource, e.g. `get nodes`, so the operator can fix the role). A missing object is a normal result, not an error. Error messages never echo bearer tokens or raw kubeconfig contents. There is no startup connectivity probe gate, because it would say nothing about whether a later call succeeds.

## Testing

Shared code is table-tested once: target validation (empty, blank, duplicate, over each cap, order, namespaced fields), the parallel fetch (bounded concurrency, error naming the target, cancellation of in-flight and queued targets after a failure), the missing-namespace check, pod status precedence, YAML cleaning, and redaction at its length boundary with `valueFrom` untouched. Connection resolution is tested for the explicit-kubeconfig, context, in-cluster (via an injectable seam), and development-fallback branches.

Each resource's tools are tested with client-go's fake clientset and table-driven fixtures, asserting both text and structured output, and each per-target tool is called with a mix of existing and missing targets. Because the fake clientset does not reproduce API-server RBAC or network behavior, failure paths (RBAC-forbidden, timeout, transport failure) are tested with explicitly injected fake reactions.

A live acceptance runs the tools against a real cluster and confirms the output; it never runs in CI, is not a unit test, hardcodes no cluster, uses whatever kubeconfig/context the operator points it at, and the target is optional and never assumed to be production.

## Future work

Mutating operations (cordon, drain, restart, scale, delete) are deferred until Phase 4 provides per-requester authorization, Slack approval, and durable audit; they will be ordinary tools in the same group, marked non-read-only and gated by policy, not by group membership.

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
13. Connection: explicit `kubeconfig` never silently falls back, in-cluster config when running as a pod, local kubeconfig as the development fallback; an explicit `context` forces kubeconfig resolution. Every group setting lives in the optional `--kubernetes-config` file.
14. The `rest.Config` sets a finite request timeout and a `chatops-mcp/<version>` user agent.
15. Network failures are runtime tool errors; only local config problems fail startup.
16. RBAC is the only switch for features that need a powerful grant; such grants stay out of the baseline.
17. `list` tools and each `pods` target return at most `max_results` rows (default 50), capped after listing, with the total reported; no pagination.
