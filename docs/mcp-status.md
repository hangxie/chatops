# status MCP tool group

The `status` group reports the status and active incidents of services, from their vendors' public status pages or, for self-hosted services, their own health endpoints. It gives the chat bot a useful read-only tool that needs only outbound HTTP(S) and no credentials. Package: `pkg/mcpserver/status`.

## Tool: `service_status`

- Argument: `service` (required) — one configured service name, or `all` for every one. The argument is a JSON Schema enum of the configured names, so a small model can map a question such as "is gmail down?" to the right service; a service not in the enum is reported as not covered rather than guessed at.
- Result text: one line per service, `[HEALTH] <display> - <summary>`, with each active incident indented beneath it (name, status, URL). Structured output carries the same snapshots.
- Health labels: `OK`, `MAINTENANCE`, `DEGRADED`, `PARTIAL OUTAGE`, `MAJOR OUTAGE`, `UNKNOWN`. A status page that cannot be read is reported as `UNKNOWN` health rather than failing the call, since the vendor's service may well be up; an `http` health endpoint that cannot be reached is reported as `MAJOR OUTAGE`, since the endpoint is the service itself.
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
| `datadog` | Datadog Status Pages `<status page>/config.json`; the page itself is rendered by JavaScript, so this file is the only machine-readable source |
| `google` | Google Cloud or Workspace `incidents.json` feeds, combined and optionally narrowed to product IDs |
| `http` | A self-hosted health endpoint such as llama.cpp `/health`, Harbor `/api/v2.0/health`, or chatops `/healthz`; see below |

An `http` entry is `OK` on any 2xx response, unless its JSON body reports a problem; then it is `PARTIAL OUTAGE`. A `status` field reports one when it is present and is neither `true` nor `ok`, `healthy`, `up`, or `pass` (case-insensitive), so `false` or `500` counts while a missing or `null` one leaves the verdict to the HTTP code. A `components` list (as Harbor reports) reports one for each member whose `status` is not one of those words, listed as an incident, and the summary counts them when the top-level status is healthy. Fields are read one by one, so a malformed component or a `components` value that is not a list never hides the top-level `status`. A non-2xx response, or none at all, is `MAJOR OUTAGE`, and the summary quotes the start of the response body so the reason a service gives for failing reaches the user.

Unknown fields, duplicate names, and URLs that are not absolute `http`/`https` stop `chatops-mcp` at startup.
