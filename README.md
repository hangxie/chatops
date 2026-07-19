# chatops

`chatops` connects a chat backend to a planner and operational tools, then processes messages until interrupted.

## Quick start

This local test uses the built-in telnet chat backend and ping planner. It requires Go and netcat (`nc`) and does not require a credentials file.

From the repository root, start a TCP listener in the first terminal:

```bash
nc -l 6023
```

In a second terminal, start the server:

```bash
go run . server \
    --chat telnet://localhost:6023 \
    --planner ping://
```

Return to the netcat terminal, type `ping`, and press Enter:

```text
ping
pong
```

You can also exercise the planner's confirmation flow:

```text
can you ping the server?
do you want me to ping? (yes/no)
yes
pong
```

Press Ctrl+C in the server terminal to stop gracefully. Closing netcat instead disconnects the chat backend and causes the server to exit with a connection-loss error.

## Usage

```console
$ chatops --help
Usage: chatops <command>

Commands:
  server     Run the ChatOps server.
  version    Show build version.
```

### Server

The server requires one chat backend URL and one planner URL:

```bash
chatops server --chat telnet://localhost:6023 --planner ping://
```

Available settings:

| Flag | Required | Default | Description |
| --- | --- | --- | --- |
| `--chat` | Yes | — | Chat backend URL, such as `telnet://localhost:6023`. |
| `--planner` | Yes | — | Planner backend URL, such as `ping://`. |
| `--credentials` | No | None | Credential store URL used by planners and tools. |
| `--connection-id` | No | `default` | Stable identifier used to scope planner conversation state. |
| `--max-concurrency` | No | `8` | Maximum conversations processed concurrently; the maximum value is `256`. |

A fully configured invocation looks like this:

```bash
chatops server \
    --chat telnet://chat.example.com:6023 \
    --planner ping:// \
    --credentials json-file:///etc/chatops/credentials.json \
    --connection-id operations \
    --max-concurrency 8
```

The current server supports these URLs:

| Component | Scheme | URL form | Notes |
| --- | --- | --- | --- |
| Chat | `telnet` | `telnet://host:port` | Port defaults to `23`; the protocol is newline-delimited text without telnet option negotiation. |
| Planner | `ping` | `ping://` | Recognizes ping requests and requires no credentials. |
| Credentials | `json-file` | `json-file:///path/to/file.json` | Optional JSON object mapping credential names to string values. |

The server also wires the `ping://` operational tool and its internal `reply://` tool. The first SIGINT or SIGTERM cancels in-flight work and closes resources gracefully; a second signal uses the operating system's default handling.

### Credential file

The `json-file` credential store expects a single JSON object whose values are strings:

```json
{
  "db-password": "example-password",
  "api-token": "example-token"
}
```

Credential values do not belong in backend, planner, or tool URLs. Backends that require authentication resolve it from the credential store or their standard environment variables.

### Version

Print the short version or detailed build metadata:

```console
$ chatops version
v0.1.0

$ chatops version --all --json
{"Version":"v0.1.0","BuildTime":"2026-07-15T22:04:35-0700","Source":"github"}
```

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for package architecture, Go API examples, and instructions for adding credential stores, chat backends, tools, and planners.
