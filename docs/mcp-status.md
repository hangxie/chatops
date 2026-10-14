# status MCP tool group

The `status` group reports the public status and active incidents of external services from their vendors' status pages. It gives the chat bot a useful read-only tool that needs only outbound HTTPS and no credentials. Package: `pkg/mcpserver/status`.

## Tool: `service_status`

- Argument: `service` (required) — one configured service name, or `all` for every one. The argument is a JSON Schema enum of the configured names, so a small model can map a question such as "is gmail down?" to the right service; a service not in the enum is reported as not covered rather than guessed at.
- Result text: one line per service, `[HEALTH] <display> - <summary>`, with each active incident indented beneath it (name, status, URL). Structured output carries the same snapshots.
- Health labels: `OK`, `MAINTENANCE`, `DEGRADED`, `PARTIAL OUTAGE`, `MAJOR OUTAGE`, `UNKNOWN`. A status page that cannot be read is reported as `UNKNOWN` health rather than failing the call.
- Annotations: `ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: true` (it reaches external pages).

No service, URL, or product name is hardcoded: the code knows only the page formats, and every service comes from the config file. The tool description tells the model that only the listed services are covered.

## Configuration

Services are listed in the `--status-config` YAML file (required with `--tools status`), shipped as `package/systemd/mcp-status.yaml` and installed at `/etc/chatops/mcp-status.yaml`. Services are reported in file order; restart the server after editing. Each entry has:

- `name` — the tool argument value (lowercase letters, digits, `-`).
- `display` — the name shown in results.
- `covers` — the product names users ask about; these drive the enum description, so when the bot answers from the wrong service or not at all, add the product name here.
- `type` and its endpoint `url` (see below). A `google` entry instead lists `feeds`, each a `url` with an optional `products` filter.

| `type` | Endpoint |
|---|---|
| `statuspage` | Atlassian Statuspage `.../api/v2/summary.json`, used by many vendors |
| `statusio` | Status.io `https://api.status.io/1.0/status/<page id>` |
| `slack` | `https://slack-status.com/api/v2.0.0/current` |
| `google` | Google Cloud or Workspace `incidents.json` feeds, combined and optionally narrowed to product IDs |

Unknown fields, duplicate names, and URLs that are not absolute `http`/`https` stop `chatops-mcp` at startup.
