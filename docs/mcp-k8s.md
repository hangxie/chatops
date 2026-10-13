# kubernetes MCP tool group

Status: implemented ([`DESIGN.md`](DESIGN.md) Phase 3, node slice). Selectable with `--tools kubernetes`.

The `kubernetes` group is ChatOps' Kubernetes integration: a single tool group organized internally by resource, exposing read-only tools. The first slice ships three node tools — `node_list`, `node_status`, and `node_resources`; deployment and pod tools follow the same shape later. All mutation is out of scope until the Phase 4 authorization and approval framework exists. Package: `pkg/mcpserver/kubernetes`.

## Decision: one group, resource-oriented internals

Kubernetes is exposed as one `kubernetes` tool group whose code is organized by resource module (`node` now; `deployment`, `pod` later), with resource-first tool names (`node_list`, `node_status`, later `deployment_list`, and so on). Kubernetes is one external system with one client, one credential context, and one set of error conventions, so the MCP server boundary matches the integration boundary.

## Decision: per-object tools take a list of targets

Every tool that reports on specific objects takes a list of targets rather than a single name, and listing is always a separate tool. A question about several objects is therefore one list call plus one call for all of them — "resource utilization on all nodes" is `node_list` then a single `node_resources` with every name, and the planned "status of a deployment's pods" is a pod listing then a single `pod_status` with every pod — so the number of model rounds no longer grows with the number of objects. This matters for the small models ChatOps targets, which usually make one tool call per response and would otherwise exhaust `agent.max_iterations` on a mid-sized cluster. There is no implicit "all objects" mode: the model always names its targets, taken from the list tool.

The target list is required and must be non-empty; names are trimmed, a blank entry is an error, duplicates are dropped keeping request order, and at most 50 distinct targets are accepted per call so the combined result stays within the agent's tool-result budget. Targets are fetched in parallel, at most four at a time, and the output keeps request order. A target that does not exist is a normal `found: false` entry, so one bad name never hides the others; a failure that is not a miss (unreachable cluster, RBAC-forbidden) fails the whole call, cancels the targets still in flight or queued, and reports that first failure with the target it hit. Structured output wraps the per-target entries in a single list field named after the argument, e.g. `{"nodes": [...]}`.

Tool grouping is not an authorization boundary. Three controls stay independent: tool exposure (`--tools`), ChatOps authorization (Phase 4: roles, rules, approval, enforced at execution time), and Kubernetes RBAC (enforced by the API server). Selecting a group only affects exposure, and `ReadOnlyHint` is advisory, so the read/destructive and per-requester decisions belong to the other two controls. This slice is read-only, so RBAC reduces to granting the credentials only `get`/`list` on `nodes`, `list` on `pods` (for the pod count), and `get` on `nodes` in the `metrics.k8s.io` group (for utilization). The pod `list` is cluster-wide, so the credentials can read pod metadata across all namespaces; that exposure is wider than the node reads and should be scoped deliberately.

## Tools (node slice)

All three tools are read-only (`ReadOnlyHint: true`, `IdempotentHint: true`) with resource-first, snake_case names matching the existing `service_status` tool. Each returns a typed Go response, so `mcp.AddTool` derives the output schema and populates `StructuredContent`; the handler also sets `Content` to a concise human-readable summary rather than relying on the SDK's JSON fallback, because ChatOps targets small models that answer better from a compact text block.

Node conditions preserve the three API states — `True`, `False`, and `Unknown` — and are never collapsed; a condition absent from `Node.Status.Conditions` is represented as missing/null, distinct from an explicit `Unknown`. Roles are derived from `node-role.kubernetes.io/*` labels; a node with no such label renders as `<none>` and custom role labels are shown verbatim. Age is stored as the node's creation timestamp in structured output and rendered to a human-readable age only in text. CPU and memory are `resource.Quantity` values (memory typically in `Ki`), rendered to a readable unit in text.

### `node_list`

The cluster node roster at a glance. Arguments: none in this slice. Output is one line per node:

    NAME      STATUS   ROLES          AGE   VERSION
    master-0  Ready    control-plane  32d   v1.37.1
    worker-0  Ready    <none>         32d   v1.37.1
    worker-5  NotReady <none>         32d   v1.37.1

Structured output carries name, ready, roles, creation timestamp, and version per node.

### `node_status`

Health of one or more nodes. Argument: `nodes` (required list of node names). Output is one block per node, separated by a blank line, and reports the Ready condition, the pressure conditions (memory, disk, PID) and any others the node reports, schedulable or not, taints, kubelet version, and roles:

    [Ready] worker-4 (<none>) v1.37.1
    conditions: Ready=True MemoryPressure=False DiskPressure=False PIDPressure=False NetworkUnavailable=False
    scheduling: schedulable taints=gpu=true:NoSchedule

Conditions are reported verbatim; `node_status` is health only, so capacity and utilization live in `node_resources`. Structured output is `{"nodes": [...]}` with one entry per requested node carrying the parsed fields and a `found` boolean so clients need not parse text: a hit is `{"node": "worker-4", "found": true, ...}` and a miss is `{"node": "worker-99", "found": false}`. A missing node is a normal `found: false` result reported as not found, never a fuzzy match and not a tool error.

### `node_resources`

CPU, memory, disk, and pod capacity, allocatable, and current utilization for one or more nodes. Argument: `nodes` (required list of node names). Output is one block per node, separated by a blank line (shown with disk usage enabled):

    worker-4
    capacity:    cpu=6 memory=11.7Gi disk=97.9Gi pods=110
    allocatable: cpu=6 memory=11.6Gi disk=88.1Gi pods=110
    usage:       cpu=246m (4%) memory=6.1Gi (52%) disk=28.5Gi (32%) pods=9 (8%)

Each figure is drawn from the narrowest authoritative source:

| Figure | Source |
|---|---|
| CPU / memory / disk / pods capacity and allocatable | Node API (disk is `ephemeral-storage`) |
| CPU and memory utilization | metrics API (`metrics.k8s.io`) |
| Pod count | Pod API (live count of non-terminated pods on the node) |
| Disk utilization | kubelet Summary API — needs extra RBAC, see below |

CPU, memory, and pod utilization are shown as a percentage of allocatable. Disk is the exception: its usage comes from the kubelet filesystem (see below), so it is shown as a percentage of that filesystem's total, not of the ephemeral-storage allocatable — filesystem used bytes and the scheduling allocatable are different accounting boundaries, and conflating them can report misleading figures (even above 100%). The tool degrades rather than failing: if the metrics API is unavailable (no metrics-server), CPU and memory usage are omitted with a `usage_note`; if the pod API fails, the pod count is omitted with a `usage_note`; the rest of the report still returns. Only a failure to fetch the node itself is fatal.

Disk utilization is always attempted and gated only by RBAC: `node_resources` reads the node's filesystem usage from the kubelet Summary API, proxied through the API server, which needs the `nodes/proxy` permission (see security note below). When that is not granted, `usage disk=n/a` and the node's `usage_note` says disk utilization is not permitted and names the grant; a Summary API response without node filesystem usage, or any other failure such as a timeout, is noted too. Disk trouble never costs the CPU, memory, or pod figures, and each node in a call degrades independently. That reports Kubernetes-managed filesystem usage only (the kubelet `node.fs`), not every host-mounted disk — a separate Ceph OSD or other host mount will not appear. For comprehensive host disk monitoring, Prometheus with node-exporter is the right tool; the Summary API here is a lightweight signal for kubelet/node filesystem pressure.

Structured output is `{"nodes": [...]}`; each entry carries `capacity`, `allocatable`, and `usage` amount objects plus a `found` boolean; quantities are kept canonical in structured output and humanized in text (CPU as millicores, memory/disk as GiB). When disk usage is collected, `usage.disk` is the kubelet filesystem used bytes and `disk_total` is that filesystem's capacity in bytes — the matching denominator, distinct from the ephemeral-storage `capacity.disk`/`allocatable.disk`; when it is not collected, both are absent.

## Cluster connection and credentials

Connection resolution (standard client-go), in order: an explicit `--kubeconfig` is honored exactly and never silently falls back (a missing or invalid file fails startup); otherwise in-cluster configuration is used when running as a pod; otherwise a local kubeconfig (`$KUBECONFIG`, else `~/.kube/config`) is the development fallback. An optional `--kube-context` selects a context and explicitly requests kubeconfig-based resolution, taking precedence over in-cluster detection so it is never silently ignored in-cluster. These flags live on `chatops-mcp serve`, gated so they are only valid with `--tools kubernetes` (the same pattern as `--status-config`).

When the `rest.Config` is built, two client-level defaults are set: a finite request timeout as a backstop to the per-call context deadline, and a `chatops-mcp/<version>` user agent so requests are identifiable in the API server's audit logs. The core and metrics clients are both built from this one config. The HTTP bearer token (`--token-env`) only authenticates the MCP client to `chatops-mcp`; it says nothing about what the Kubernetes credentials may do.

The baseline RBAC is small and read-only: `get`/`list` on `nodes`, `list` on `pods`, and `get` on `nodes` in `metrics.k8s.io`. A sample `ClusterRole` and binding ship with the README. Disk utilization additionally requires `get` on the `nodes/proxy` subresource, which is deliberately kept out of the baseline: `nodes/proxy` is a powerful grant that reaches every kubelet endpoint, not just the Summary API, so it should be added only when node disk reporting is actually wanted — especially as mutating tools arrive in later phases. Granting it is the operator's explicit choice, and that grant is the only switch: there is no separate server flag that could disagree with it.

## Package layout

Files under `pkg/mcpserver/kubernetes/` (each with a one-to-one `_test.go`): `kubernetes.go` (group `Register`, tool definitions, shared handler plumbing); `client.go` (kubeconfig/in-cluster resolution and construction of the core and metrics clients); `node.go` (list and status logic against the typed API); `resources.go` (capacity/allocatable/utilization logic); `format.go` (text rendering); `targets.go` (target-list validation and bounded parallel fetch shared by every per-object tool). Wiring in `cmd/chatops-mcp/serve/serve.go` adds `"kubernetes"` to the `groups` map and the `--kubeconfig`/`--kube-context` flags with the same gating as `--status-config`. New dependencies: `k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery`, and `k8s.io/metrics`.

## Error handling

Local configuration errors fail `chatops-mcp serve` fast, consistent with how `status` validates its config at load: a bad kubeconfig path, a malformed kubeconfig, or an unknown `--kube-context`. Everything network-dependent is a runtime tool error the model can relay, because a cluster can become unreachable at any time: cluster unreachable or TLS failure, API-call timeout, and RBAC-forbidden (name the verb and resource, e.g. `get nodes`, so the operator can fix the role). A missing node is a normal result, not an error. Error messages never echo bearer tokens or raw kubeconfig contents. There is no startup connectivity probe gate, because it would say nothing about whether a later call succeeds.

## Testing

Target-list handling is table-tested (empty, blank, duplicate, over-limit, order, bounded concurrency, error naming the target, cancellation of in-flight and queued targets after a failure), and each per-object tool is called with a mix of existing and missing targets. Unit tests use client-go's fake clientset (and the metrics fake clientset) with table-driven node fixtures (ready, not-ready, and `Unknown`-Ready; control-plane, worker, custom-role, and no-role; pressure conditions set and unset; missing node) asserting both text and structured output, plus pod fixtures for the count, a metrics-unavailable path, and table-driven Summary API outcomes (used and total, missing capacity, missing node filesystem, forbidden, timeout, other error) that must each keep the other figures, plus a call mixing a permitted and a forbidden node. Flag and connection-config resolution are tested for the explicit-kubeconfig, context, in-cluster, and development-fallback branches (the in-cluster branch via an injectable seam). Because the fake clientset does not reproduce API-server RBAC or network behavior, the failure paths (RBAC-forbidden, API timeout, transport failure) are tested with explicitly injected fake reactions. A live acceptance runs the three tools against a real cluster and confirms the output; it never runs in CI, is not a unit test, hardcodes no cluster, uses whatever kubeconfig/context the operator points it at, and the target is optional and never assumed to be production.

## Future work

Large clusters will eventually need the node list capped or paginated so a big roster does not overflow the model's context; the first slice assumes a homelab-sized cluster and returns every node. Host disk monitoring beyond the kubelet filesystem (separate data disks, Ceph OSDs) is out of scope for the Summary API and belongs to Prometheus/node-exporter. `deployment` and `pod` modules (`deployment_list`, `deployment_status`, `pod_list`, `pod_status`) come next, following the list-of-targets rule; pods are namespaced, so their targets will carry a namespace as well as a name, then diagnostics such as `pod_logs` and events. Mutating operations (cordon, drain, restart, scale, delete) are deferred until Phase 4 provides per-requester authorization, Slack approval, and durable audit; they will be ordinary tools in the same group, marked non-read-only and gated by policy, not by group membership.

## Decisions

1. Kubernetes is one MCP tool group (`kubernetes`), organized internally by resource; the MCP server boundary matches the integration boundary.
2. Tool names are resource-first and snake_case (`node_list`, `node_status`, `node_resources`).
3. The first slice is read-only node information; no mutation until Phase 4.
4. `node_status` is health only; capacity, allocatable, and utilization are a separate `node_resources` tool. CPU/memory utilization come from the metrics API and the pod count from the pod API, each degrading to a `usage_note` on failure (only the node fetch is fatal). Disk utilization comes from the kubelet Summary API, enabled solely by granting the powerful `nodes/proxy` permission, which is kept out of the baseline RBAC (a forbidden response becomes a per-node "not permitted" note); it reports kubelet filesystem usage only, not arbitrary host disks, and is shown as a percentage of that filesystem's own capacity rather than the ephemeral-storage allocatable.
5. Tool grouping is not an authorization boundary; exposure, ChatOps authorization, and Kubernetes RBAC are three independent controls.
6. Connection: explicit `--kubeconfig` never silently falls back, in-cluster config when running as a pod, local kubeconfig as the development fallback.
7. Node conditions preserve `True`/`False`/`Unknown` and are never collapsed; an absent condition is missing/null, not a manufactured `Unknown`; capacity and allocatable are node facts, not live usage.
8. Age is stored as a creation timestamp in structured output and rendered to a human-readable age only in text.
9. `node_status` and `node_resources` structured output carry a `found` boolean per node so clients need not parse text.
10. An explicit `--kube-context` requests kubeconfig-based resolution and takes precedence over in-cluster detection.
11. Network failures are runtime tool errors, not startup failures; only local config problems fail startup, and failure paths are tested with injected fake reactions.
12. The `rest.Config` sets a finite request timeout and a `chatops-mcp/<version>` user agent.
12. Per-object tools take a required list of targets (at most 50, deduplicated, fetched four at a time, output in request order) and listing is a separate tool, so a question about many objects costs two tool calls; there is no implicit all-objects mode, and a missing target is a `found: false` entry, not a call failure.
