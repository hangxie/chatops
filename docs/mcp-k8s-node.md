# kubernetes: node

Status: implemented. Part of the [`kubernetes`](mcp-k8s.md) tool group, whose conventions apply here.

`node_status` is health only (conditions, schedulability, taints, version, roles); capacity and utilization are a separate `node_resources` tool, so a health question never pays for metrics calls and a utilization failure never hides health. A node's overall state is its `Ready` condition, `Unknown` when absent.

## Utilization sources

Each `node_resources` figure comes from the narrowest authoritative source, and each degrades independently:

| Figure | Source | On failure |
|---|---|---|
| Capacity and allocatable (disk is `ephemeral-storage`) | Node API | the call fails |
| CPU and memory usage | metrics API (`metrics.k8s.io`) | omitted with a `usage_note` |
| Pod count | Pod API, non-terminated pods on the node | omitted with a `usage_note` |
| Disk usage | kubelet Summary API via `nodes/proxy` | omitted with a per-node `usage_note` naming the cause |

CPU, memory, and pods are shown as a percentage of allocatable. Disk is shown as a percentage of the kubelet filesystem's own total (`disk_total` in structured output), not of ephemeral-storage allocatable: filesystem used bytes and the scheduling allocatable are different accounting boundaries, and mixing them can report figures above 100%. It covers the kubelet `node.fs` only, not other host disks such as Ceph OSDs, which belong to Prometheus with node-exporter.

## RBAC

`get`/`list` on `nodes`, `list` on `pods` (cluster-wide, for the pod count), and `get` on `nodes` in `metrics.k8s.io`. Disk usage additionally needs `get` on `nodes/proxy`, kept out of the baseline because it reaches every kubelet endpoint, not just the Summary API. Granting it is the only switch: without it each node notes that disk utilization is not permitted and names the grant.
