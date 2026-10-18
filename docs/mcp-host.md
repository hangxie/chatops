# host MCP tool group

Status: implemented. Selectable with `--tool host`.

The `host` group answers "is this machine reachable?" for hosts the administrator lists. It resolves the name, pings it, and checks that SSH answers. Package: `pkg/mcpserver/host`.

It complements [`status`](mcp-status.md), which checks the services on those hosts through their health endpoints. Reachability does not belong there: it is not HTTP, and a host has no status page.

## Tool: `host_status`

- Argument: `hosts` (required), a non-empty list of configured host names, following the target-list convention of [`mcp-k8s.md`](mcp-k8s.md#targets). "Are media and pi up?" is one call.
- Item names are a JSON Schema enum of the configured hosts, each described with its address and `covers`, as `service_status` does. The address lets the model tell an unlisted name such as `nas.example.com` from a listed host, and the result repeats it, so a wrong mapping shows in the reply. `all` checks every host.
- Result text: one line per host, `[HEALTH] <display> (<address>) - <summary>`, with the configured address left out when it equals the display name, then one indented line per check with its outcome (resolved and selected address, ping loss and round-trip time, SSH banner).
- Structured output carries the same per-check results.
- Annotations: `ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: true`.

One tool runs every check, rather than one tool per check: users ask about the host, and a small model answers best from one call.

The description says the tool checks the machine only, not what runs on it, and never names chatops. In the model check, qwen-3.5-4b sent "is the chat bot healthy?" and "is llama.cpp on media running?" to `host_status`, probably because the first matched "the ChatOps server" in the old description and the second the host name.

## Only configured hosts

The tool never takes a free-form address. A bot that pings any name a user types is a network scanner for everyone in the channel, reaching hosts behind its own firewall. Hosts come from the `--config` directory, as `status` services do.

## Checks

The result reports what `chatops-mcp` observed from where it runs, never more. A failed probe means the host did not answer that probe from here, not that it is powered off.

Each check ends in one outcome:

| Outcome | Meaning |
|---|---|
| `PASS` | The host answered as expected. |
| `FAIL` | The probe ran and the host did not answer as expected (timeout, refused, wrong reply). |
| `ERROR` | The checker could not run the probe (socket not permitted, no local route, local resource failure). Says nothing about the host. |

A failed connection is classified by its error:

| Error | Outcome | Why |
|---|---|---|
| `ENETUNREACH` "network is unreachable" | `ERROR` | This machine has no route. |
| `EHOSTUNREACH` "no route to host" | `FAIL` | A powered-off host on the local network that does not answer ARP, or a router reporting on the target. |
| `EHOSTDOWN` "host is down" (macOS) | `FAIL` | Same as above. |

Order and limits:

```text
resolve (3s) ──▶ ping (3s) ─┐
             └─▶ ssh  (3s) ─┴─▶ health        at most 4 hosts at a time
```

- Every host is resolved first; `ping` and `ssh` then run concurrently against the same address.
- The tool call honors context cancellation, so a client giving up stops every outstanding probe.
- Worst case for one host is about 6 seconds. Answering hosts finish in milliseconds, so `all` is slow only when hosts are unreachable.

### resolve

- Uses the system resolver, returning every address and the canonical name, so an alias is visible (`ai.xiehang.com` → `mbp16.xiehang.com`).
- One address is selected for the probes: the first IPv4, else the first IPv6. Ping and SSH can never reach different machines or address families.
- The result reports the selected address and the full list. Probing every address is left for later.
- A failure skips the other checks, with distinct summaries so a config mistake is not mistaken for a DNS outage:

| Resolver result | Summary |
|---|---|
| NXDOMAIN | "name does not exist in DNS" |
| timeout, `SERVFAIL`, other error | "DNS lookup failed" |

### ping

- Three ICMP echoes over an unprivileged ICMP socket (`golang.org/x/net/icmp`, network `udp4`/`udp6`), reporting loss and average round-trip time.
- `PASS` when any echo is answered. A target whose firewall drops echo requests is a `FAIL`.
- Permission matters only where `chatops-mcp` runs, which is Linux. It needs no root and no `CAP_NET_RAW` when `net.ipv4.ping_group_range` (which also governs ICMPv6) includes the server's group. Recent systemd sets it for every group. Other operating systems are not targeted.
- When the socket cannot be created the check is an `ERROR` ("ICMP not permitted"). It never falls back to privileged raw sockets.
- Each probe opens its own socket. A reply counts only when it is an echo reply whose identifier, sequence number, and source match the request. Anything else, including the request echoed back on loopback (seen on macOS IPv6), is ignored until the deadline, so concurrent probes never take each other's replies.

### ssh

- A TCP connection to the SSH port that reads the server's identification and closes. It sends nothing and never authenticates, so it needs no credentials.
- One 3-second deadline covers connecting and reading.
- Parsing follows RFC 4253: lines before the one starting with `SSH-` are skipped, each line is at most 255 bytes, and at most 8 KiB is read.

| Reply | Outcome |
|---|---|
| `SSH-2.0-<software>` | `PASS`, software version reported |
| `SSH-1.99-<software>` (also speaks 2.0, RFC 4253 §5.1) | `PASS` |
| other `SSH-` line (`SSH-1.5-...`, `SSH-garbage`) | `FAIL` |
| refused, or not an SSH identification | `FAIL`, but the summary says the host answered |

sshd logs each check as a connection closed before authentication, and fail2ban's `aggressive` sshd mode counts those, so allow-list the `chatops-mcp` host there. Tested against OpenSSH and Dropbear, which both send their identification first.

## Health

Health comes only from the outcomes of the enabled checks after a successful resolve. The summary states the observation (for example "no probe answered from chatops-mcp"), not a conclusion about the machine.

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

The worst label is `UNREACHABLE`, not `DOWN`: probes from one place cannot prove a machine is off, and the model repeats the label it is given.

Probe outcomes and the health rule are separate functions, with the ICMP and SSH checkers injected, so the aggregation is tested without a network.

## Configuration

- `--config` is required with `--tool host` and names a directory, installed as `/etc/chatops/mcp-host.d/`.
- Its `00-default.yaml` ships one host, `localhost` with the ping check only, so the packaged `host` server starts out of the box. Other hosts are site-specific and stay commented examples.
- Hosts go in a file of your own beside it, merged in file name order as `status` reads `mcp-status.d/`.
- Unknown fields and duplicate names (also across files) stop `chatops-mcp` at startup.
- Host `covers` should name the machine, not what runs on it. A host covering "the LLM host" pulls "is the LLM up?" to `host_status`; service names belong in the `status` service's `covers`.

```yaml
hosts:
  - name: media              # tool argument value; lowercase letters, digits, "-"
    display: media
    covers: [media server, the media box]
    address: media.xiehang.com
  - name: ai
    display: ai
    covers: [the Mac, the MacBook]
    address: ai.xiehang.com
    checks: [ping, ssh]      # default; resolve always runs
    ssh_port: 22             # default
```
