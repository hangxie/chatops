# host MCP tool group

Status: planned. Selectable with `--tool host` once implemented.

The `host` group answers "is this machine reachable?" for hosts the administrator lists: it resolves the name, pings it, and checks that SSH answers. It complements `status`, which checks the services running on those hosts through their health endpoints ([`mcp-status.md`](mcp-status.md)); reachability does not belong there because it is not HTTP and a host has no status page. Package: `pkg/mcpserver/host`.

## Tool: `host_status`

- Argument: `hosts` (required) — a non-empty list of configured host names, following the target-list convention of [`mcp-k8s.md`](mcp-k8s.md#targets), so "are media and pi up?" is one call. Item names are a JSON Schema enum of the configured hosts, each described with its `covers`, as `service_status` does; `all` checks every host.
- Result text: one line per host, `[HEALTH] <display> - <summary>`, with one indented line per check and its outcome (resolved and selected address, ping loss and round-trip time, SSH banner). Structured output carries the same per-check results.
- Annotations: `ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: true`.

One tool runs every check rather than one tool per check, because the question users ask is about the host, and a small model answers it best from one call.

## Only configured hosts

The tool never takes a free-form address. A chat bot that pings or connects to any name a user types is a network scanner for everyone in the channel, reaching hosts behind the firewall it runs inside. Hosts come from the `--config` directory, as `status` services do.

## Checks

The result reports what `chatops-mcp` observed from where it runs, never more: a failed probe means the host did not answer that probe from here, not that it is powered off. Each check ends in one outcome:

- `PASS` — the host answered as expected.
- `FAIL` — the probe ran and the host did not answer as expected (timeout, refused, wrong reply).
- `ERROR` — the checker itself could not run the probe (socket not permitted, no local route to the network, local resource failure), which says nothing about the host.

A failed connection is classified by its error: "network is unreachable" (`ENETUNREACH`) means this machine has no route, an `ERROR`; "no route to host" (`EHOSTUNREACH`) and "host is down" (`EHOSTDOWN`, macOS) are what a powered-off host on the local network produces when it does not answer ARP, or a router reports about the target, so they are a `FAIL`.

Every host is resolved first; `ping` and `ssh` then run concurrently against the same address, each with a 3-second timeout, and hosts are checked at most four at a time.

- **resolve** — the system resolver, with its own 3-second timeout, returning every address and the canonical name, so an alias is visible (`ai.xiehang.com` → `mbp16.xiehang.com`). One address is selected for the probes, the first IPv4 address, else the first IPv6 one, so ping and SSH can never reach different machines or address families. The result reports the selected address and the full list; probing every address is left for later. A name that does not resolve, or a resolver error, skips the other checks, with distinct summaries so a configuration mistake is not mistaken for a DNS outage: "name does not exist in DNS" for NXDOMAIN, "DNS lookup failed" for a timeout, `SERVFAIL`, or other resolver error.
- **ping** — three ICMP echoes over an unprivileged ICMP socket (`golang.org/x/net/icmp`, network `udp4`/`udp6`), reporting loss and average round-trip time; `PASS` when any echo is answered. The permission is needed only where `chatops-mcp` runs, which is Linux: it needs no root and no `CAP_NET_RAW` when `net.ipv4.ping_group_range` (which also governs ICMPv6) includes the server's group, which recent systemd sets for every group by default. Other operating systems are not targeted. Each probe opens its own socket, and a reply counts only when it is an echo reply whose identifier, sequence number, and source match the request and the selected address; anything else, including the request itself echoed back on loopback (seen on macOS IPv6), is ignored until the deadline, so concurrent probes never take each other's replies. When the socket cannot be created the check is an `ERROR` ("ICMP not permitted"); it never falls back to privileged raw sockets. The target needs nothing but to answer echo requests; one whose firewall drops them is a `FAIL`.
- **ssh** — a TCP connection to the SSH port that reads the server's identification and closes, sending nothing and never authenticating, so it needs no credentials. A 3-second deadline covers connecting and reading together. Following RFC 4253, lines before the one starting with `SSH-` are skipped, each line is at most 255 bytes, and at most 8 KiB is read; `PASS` needs a valid identification, `SSH-2.0-<software>` or `SSH-1.99-<software>` (a server that also speaks 2.0, per RFC 4253 section 5.1), whose software version is reported; any other line starting with `SSH-`, such as `SSH-1.5-...` or `SSH-garbage`, is a `FAIL`. A refused connection or a reply that is not an SSH identification is a `FAIL`, but the summary says the host answered, since something at that address responded. sshd logs each check as a connection closed before authentication, and fail2ban's `aggressive` sshd mode counts those, so allow-list the `chatops-mcp` host there. Implementation is tested against OpenSSH and Dropbear, which both send their identification first.

The tool call honors context cancellation, so a client giving up stops every outstanding probe. Worst case for one host is about 6 seconds (resolve plus probes); answering hosts finish in milliseconds, so `all` is slow only when hosts are unreachable.

## Health

Health is derived only from the outcomes of the enabled checks after a successful resolve, and the summary states the observation (for example "no probe answered from chatops-mcp") rather than a conclusion about the machine:

| Check outcomes | Health |
|---|---|
| all `PASS` | `UP` |
| `PASS` with any `FAIL` or `ERROR` | `DEGRADED` |
| `FAIL` only, and at least one shows the host answered (SSH refused) | `DEGRADED` |
| all `FAIL`, none answered | `UNREACHABLE` |
| `FAIL` and `ERROR`, no `PASS` | `UNKNOWN` |
| all `ERROR` | `UNKNOWN` |
| name did not resolve, or resolver error | `UNKNOWN` |

| ping | ssh | Health |
|---|---|---|
| `PASS` | `PASS` | `UP` |
| `PASS` | `FAIL` (refused) | `DEGRADED` |
| `FAIL` (timeout) | `PASS` | `DEGRADED`; a firewall dropping ICMP looks like this, so disable `ping` for that host |
| `FAIL` | `FAIL` (refused) | `DEGRADED`; the host answered |
| `FAIL` | `FAIL` (timeout) | `UNREACHABLE` |
| `ERROR` (not permitted) | `PASS` | `DEGRADED` |
| `ERROR` | `FAIL` | `UNKNOWN` |
| `ERROR` | disabled | `UNKNOWN` |

The worst label is `UNREACHABLE`, not `DOWN`: probes from one place cannot prove a machine is off, and the model repeats the label it is given. Probe outcomes and the health rule are separate functions, with the ICMP and SSH checkers injected, so the aggregation is tested without a network.

## Configuration

`--config` (required with `--tool host`) names a directory, installed as `/etc/chatops/mcp-host.d/` with a `00-default.yaml` of commented examples only, since every host is site-specific. Hosts go in a file of your own beside it, merged in file name order as `status` reads `mcp-status.d/`. Unknown fields and duplicate names, also across files, stop `chatops-mcp` at startup.

```yaml
hosts:
  - name: media              # tool argument value; lowercase letters, digits, "-"
    display: media
    covers: [media server, the Harbor and LLM host]
    address: media.xiehang.com
  - name: ai
    display: ai
    covers: [the Mac running the LLM and TTS]
    address: ai.xiehang.com
    checks: [ping, ssh]      # default; resolve always runs
    ssh_port: 22             # default
```
