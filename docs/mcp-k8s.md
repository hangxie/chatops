# kubernetes MCP tool group

Status: design for [`DESIGN.md`](DESIGN.md) Phase 3, in progress and not yet implemented. The tools, flags, and package layout below are the plan and the design of record until the code merges; it is not yet selectable with `--tools`.

The `kubernetes` group is ChatOps' Kubernetes integration. It will be a single tool group organized internally by resource, exposing read-only tools. The first slice will ship two node tools, `node_list` and `node_status`; deployment and pod tools follow the same shape later. All mutation is out of scope until the Phase 4 authorization and approval framework exists. Package: `pkg/mcpserver/kubernetes`.

## Decision: one group, resource-oriented internals

Kubernetes is exposed as one `kubernetes` tool group whose code is organized by resource module (`node` now; `deployment`, `pod` later), with resource-first tool names (`node_list`, `node_status`, later `deployment_list`, and so on). Kubernetes is one external system with one client, one credential context, and one set of error conventions, so the MCP server boundary matches the integration boundary.

Tool grouping is not an authorization boundary. Three controls stay independent: tool exposure (`--tools`), ChatOps authorization (Phase 4: roles, rules, approval, enforced at execution time), and Kubernetes RBAC (enforced by the API server). Selecting a group only affects exposure, and `ReadOnlyHint` is advisory, so the read/destructive and per-requester decisions belong to the other two controls. This slice is read-only, so for now RBAC reduces to granting the credentials only `get`/`list` on `nodes`.

## Tools (node slice)

Both tools are read-only (`ReadOnlyHint: true`, `IdempotentHint: true`) with resource-first, snake_case names matching the existing `service_status` tool. Each returns a typed Go response, so `mcp.AddTool` derives the output schema and populates `StructuredContent`; the handler also sets `Content` to a concise human-readable summary rather than relying on the SDK's JSON fallback, because ChatOps targets small models that answer better from a compact text block.

Node conditions preserve the three API states — `True`, `False`, and `Unknown` — and are never collapsed; a condition absent from `Node.Status.Conditions` is represented as missing/null, distinct from an explicit `Unknown`. Roles are derived from `node-role.kubernetes.io/*` labels; a node with no such label renders as `<none>` and custom role labels are shown verbatim. Age is stored as the node's creation timestamp in structured output and rendered to a human-readable age only in text. CPU and memory are `resource.Quantity` values (memory typically in `Ki`), rendered to a readable unit in text.

### `node_list`

The cluster node roster at a glance. Arguments: none in this slice. Output is one line per node:

    NAME      STATUS   ROLES          AGE   VERSION
    master-0  Ready    control-plane  32d   v1.37.1
    worker-0  Ready    <none>         32d   v1.37.1
    worker-5  NotReady <none>         32d   v1.37.1

Structured output carries name, ready, roles, creation timestamp, and version per node.

### `node_status`

Detailed health of one node. Argument: `node` (required). Output summarizes readiness, roles, version, conditions, scheduling state, taints, and capacity vs allocatable:

    [Ready] worker-0 (<none>) v1.37.1
    conditions: Ready=True MemoryPressure=False DiskPressure=False PIDPressure=False NetworkUnavailable=False
    scheduling: schedulable
    capacity:    cpu=16 memory=31.3Gi pods=110
    allocatable: cpu=16 memory=31.2Gi pods=110

Capacity and allocatable are the node's total and schedulable resources as reported by the kubelet; neither is live utilization (that would need the metrics API, out of scope here). Structured output carries the parsed fields and a `found` boolean so clients need not parse text: a hit is `{"node": "worker-0", "found": true, ...}` and a miss is `{"node": "worker-99", "found": false}`. A missing node is a normal `found: false` result reported as not found, never a fuzzy match and not a tool error.

## Cluster connection and credentials

Connection resolution (standard client-go), in order: an explicit `--kubeconfig` is honored exactly and never silently falls back (a missing or invalid file fails startup); otherwise in-cluster configuration is used when running as a pod; otherwise a local kubeconfig (`$KUBECONFIG`, else `~/.kube/config`) is the development fallback. An optional `--kube-context` selects a context and explicitly requests kubeconfig-based resolution, taking precedence over in-cluster detection so it is never silently ignored in-cluster. These flags live on `chatops-mcp serve`, gated so they are only valid with `--tools kubernetes` (the same pattern as `--status-config`).

When the `rest.Config` is built, two client-level defaults are set: a finite request timeout as a backstop to the per-call context deadline, and a `chatops-mcp/<version>` user agent so requests are identifiable in the API server's audit logs. The HTTP bearer token (`--token-env`) only authenticates the MCP client to `chatops-mcp`; it says nothing about what the Kubernetes credentials may do. A sample read-only `ClusterRole` (`get`/`list` on `nodes`) and binding ship with the README.

## Package layout

Files under `pkg/mcpserver/kubernetes/` (each with a one-to-one `_test.go`): `kubernetes.go` (group `Register`, tool definitions, shared handler plumbing); `client.go` (kubeconfig/in-cluster resolution and clientset construction); `node.go` (list and status logic against the typed API); `format.go` (text rendering). Wiring in `cmd/chatops-mcp/serve/serve.go` adds `"kubernetes"` to the `groups` map and the `--kubeconfig`/`--kube-context` flags with the same gating as `--status-config`. New dependencies: `k8s.io/client-go` and `k8s.io/apimachinery`.

## Error handling

Local configuration errors fail `chatops-mcp serve` fast, consistent with how `status` validates its config at load: a bad kubeconfig path, a malformed kubeconfig, or an unknown `--kube-context`. Everything network-dependent is a runtime tool error the model can relay, because a cluster can become unreachable at any time: cluster unreachable or TLS failure, API-call timeout, and RBAC-forbidden (name the verb and resource, e.g. `get nodes`, so the operator can fix the role). A missing node is a normal result, not an error. Error messages never echo bearer tokens or raw kubeconfig contents. There is no startup connectivity probe gate, because it would say nothing about whether a later call succeeds.

## Testing

Unit tests use client-go's fake clientset with table-driven node fixtures (ready, not-ready, and `Unknown`-Ready; control-plane, worker, custom-role, and no-role; pressure conditions set and unset; missing node) asserting both text and structured output; flag and connection-config resolution are tested for the explicit-kubeconfig, context, in-cluster, and development-fallback branches. Because the fake clientset does not reproduce API-server RBAC or network behavior, the failure paths (RBAC-forbidden, API timeout, transport failure) are tested with explicitly injected fake reactions. A live acceptance runs `node_list` then `node_status <name>` against a real cluster and confirms the model calls each tool once and answers from the result; it never runs in CI, is not a unit test, hardcodes no cluster, uses whatever kubeconfig/context the operator points it at, and the target is optional and never assumed to be production.

## Future work

Large clusters will eventually need the node list capped or paginated so a big roster does not overflow the model's context; the first slice assumes a homelab-sized cluster and returns every node. A `node_resources` tool may follow if capacity reporting wants its own surface. `deployment` and `pod` modules (`deployment_list`, `deployment_status`, `pod_list`, `pod_status`) come next, then diagnostics such as `pod_logs` and events. Mutating operations (cordon, drain, restart, scale, delete) are deferred until Phase 4 provides per-requester authorization, Slack approval, and durable audit; they will be ordinary tools in the same group, marked non-read-only and gated by policy, not by group membership.

## Decisions

1. Kubernetes is one MCP tool group (`kubernetes`), organized internally by resource; the MCP server boundary matches the integration boundary.
2. Tool names are resource-first and snake_case (`node_list`, `node_status`).
3. The first slice is read-only node information; no mutation until Phase 4.
4. Tool grouping is not an authorization boundary; exposure, ChatOps authorization, and Kubernetes RBAC are three independent controls.
5. Connection: explicit `--kubeconfig` never silently falls back, in-cluster config when running as a pod, local kubeconfig as the development fallback.
6. Node conditions preserve `True`/`False`/`Unknown` and are never collapsed; an absent condition is missing/null, not a manufactured `Unknown`; capacity and allocatable are node facts, not live usage.
7. Age is stored as a creation timestamp in structured output and rendered to a human-readable age only in text.
8. `node_status` structured output carries a `found` boolean so clients need not parse text.
9. An explicit `--kube-context` requests kubeconfig-based resolution and takes precedence over in-cluster detection.
10. Network failures are runtime tool errors, not startup failures; only local config problems fail startup, and failure paths are tested with injected fake reactions.
11. The `rest.Config` sets a finite request timeout and a `chatops-mcp/<version>` user agent.
