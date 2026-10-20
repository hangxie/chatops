# status MCP tool group

The `status` group reports the status and active incidents of services. Package: `pkg/mcpserver/status`.

- External services: read from their vendors' public status pages.
- Self-hosted services: read from their own health endpoints.

It gives the bot a useful read-only tool that needs only outbound HTTP(S) and no credentials.

## Tool: `service_status`

- Argument: `services` (required), a non-empty list of configured service names. It follows the target-list convention of [`mcp-k8s.md`](mcp-k8s.md#targets): "Are Harbor and the media LLM up?" is one call.
- `all` checks every service.
- Item names are a JSON Schema enum of the configured names, so a small model can map "is gmail down?" to the right service.
- A service not in the enum is reported as not covered, rather than guessed at.
- Result text: one line per service, `[HEALTH] <display> - <summary>`. Each active incident is indented beneath it (name, status, URL).
- Structured output carries the same snapshots.
- Annotations: `ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: true` (it reaches external pages).

Health labels: `OK`, `MAINTENANCE`, `DEGRADED`, `PARTIAL OUTAGE`, `MAJOR OUTAGE`, `UNKNOWN`.

An unreadable source is treated differently by kind:

| Source cannot be read | Health | Why |
|---|---|---|
| vendor status page | `UNKNOWN` | the vendor's service may well be up |
| `http` health endpoint | `MAJOR OUTAGE` | the endpoint is the service itself |

A vendor page that cannot be read is a tool failure only when nothing was read:

| Request | Unreadable pages | Result |
|---|---|---|
| one service | its page | tool error `unable to check <display>: <error>`, which chatops lists in the reply |
| several, or `all` | every page | tool error naming the count and the first error; likely chatops's own network |
| several, or `all` | some pages | `UNKNOWN` rows; a tool error would make the model retry and lose the readable rows |

No service, URL, or product name is hardcoded.

- The code knows only page formats.
- Every service comes from the config directory.
- The tool description tells the model that only the listed services are covered.

## Configuration

- Services are listed in the `*.yaml` files of the `--config` directory, required with `--tool status`.
- Shipped as `package/systemd/mcp-status.d/00-default.yaml`, installed at `/etc/chatops/mcp-status.d/00-default.yaml`.
- Services are reported in file name order, then list order.
- Restart the server after a change.

Each entry has:

| Field | Meaning |
|---|---|
| `name` | The tool argument value (lowercase letters, digits, `-`) |
| `display` | The name shown in results |
| `covers` | Product names users ask about. They drive the enum description. When the bot answers from the wrong service, or not at all, add the product name here. |
| `type`, `url` | The page format and its endpoint (below). A `google` entry lists `feeds` instead, each a `url` with an optional `products` filter. |

| `type` | Endpoint |
|---|---|
| `statuspage` | Atlassian Statuspage `.../api/v2/summary.json`, used by many vendors |
| `statusio` | Status.io `https://api.status.io/1.0/status/<page id>` |
| `slack` | `https://slack-status.com/api/v2.0.0/current` |
| `datadog` | Datadog Status Pages `<status page>/config.json`. The page itself is rendered by JavaScript, so this file is the only machine-readable source |
| `google` | Google Cloud or Workspace `incidents.json` feeds, combined and optionally narrowed to product IDs |
| `http` | A self-hosted health endpoint such as llama.cpp `/health`, Harbor `/api/v2.0/health`, or chatops `/healthz`; see below |

Unknown fields, duplicate names (also across files), and URLs that are not absolute `http`/`https` stop `chatops-mcp` at startup.

### `http` health rule

Healthy words, case-insensitive: `true`, `ok`, `healthy`, `up`, `pass`.

| Response | Health |
|---|---|
| 2xx, body reports no problem | `OK` |
| 2xx, top-level `status` present and not a healthy word (`false`, `500`, ...) | `PARTIAL OUTAGE` |
| 2xx, a `components` member (as Harbor reports) whose `status` is not a healthy word | `PARTIAL OUTAGE`. Each such component is listed as an incident, and counted in the summary when the top-level status is healthy |
| 2xx, `status` missing or `null` | decided by the HTTP code (`OK`) |
| non-2xx, or no response | `MAJOR OUTAGE`. The summary quotes the start of the body, so the service's reason reaches the user |

Fields are read one by one, so a malformed component, or a `components` value that is not a list, never hides the top-level `status`.

### Your own services

Put site-specific services in a file of your own in the same directory, such as `10-homelab.yaml` with the same `services:` schema. Do not edit the shipped list.

- Lists are joined in file name order, by the merge rule in the README's [Configuration directories](../README.md#configuration-directories).
- Any file may be empty, as long as some service is listed.
- A later file cannot replace or remove a shipped entry, since list items are never merged.

How the deb and rpm packages treat the files on upgrade:

| File | On upgrade |
|---|---|
| `00-default.yaml`, unedited | updated |
| `00-default.yaml`, edited | kept. The new list is saved beside it, for a manual merge, as `.dpkg-dist` (if dpkg is told to keep it) or `.rpmnew`. Both are skipped |
| files you added | never touched |

Leave `00-default.yaml` unedited to keep receiving the shipped list.
