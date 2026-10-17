# kubernetes: node

Status: implemented. Part of the [`kubernetes`](mcp-k8s.md) tool group, whose conventions apply here.

| Tool | Reports |
|---|---|
| `node_status` | Health only: conditions, schedulability, taints, version, roles |
| `node_resources` | Capacity and utilization |

They are separate so a health question never pays for metrics calls, and a utilization failure never hides health. A node's overall state is its `Ready` condition, `Unknown` when absent.

## Utilization sources

Each `node_resources` figure comes from the narrowest authoritative source, and each degrades independently:

| Figure | Source | On failure |
|---|---|---|
| Capacity and allocatable (disk is `ephemeral-storage`) | Node API | the call fails |
| CPU and memory usage | metrics API (`metrics.k8s.io`) | omitted with a `usage_note` |
| Pod count | Pod API, non-terminated pods on the node | omitted with a `usage_note` |
| Disk usage | kubelet Summary API via `nodes/proxy` | omitted with a per-node `usage_note` naming the cause |

How percentages are computed:

- CPU, memory, and pods: a percentage of allocatable.
- Disk: a percentage of the kubelet filesystem's own total (`disk_total` in structured output), not of ephemeral-storage allocatable. Used bytes and scheduling allocatable are different accounting boundaries, and mixing them can report over 100%.
- Disk covers the kubelet `node.fs` only. Other host disks, such as Ceph OSDs, belong to Prometheus with node-exporter.

## RBAC

| Grant | For |
|---|---|
| `get`/`list` on `nodes` | roster and health |
| `list` on `pods` (cluster-wide) | pod count |
| `get` on `nodes` in `metrics.k8s.io` | CPU and memory usage |
| `get` on `nodes/proxy` (optional) | disk usage |

`nodes/proxy` is kept out of the baseline because it reaches every kubelet endpoint, not just the Summary API. Granting it is the only switch: without it, each node notes that disk utilization is not permitted and names the grant.
