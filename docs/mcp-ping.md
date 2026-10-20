# ping MCP tool group

The `ping` group is a harmless connectivity tool for smoke tests, and the simplest built-in group. Package: `pkg/mcpserver/ping`.

- It is the default `--tool` selection.
- It takes no `--config`.

## Tool: `ping`

- Arguments: none.
- Result: the text `pong`, with structured output `{"reply": "pong"}`.
- Annotations: `ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: false` (it touches nothing outside the process).

It exists to prove that an MCP client can reach the server and run a tool end to end, independent of any external dependency.

## Example (stdio)

```text
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"pong"}],"structuredContent":{"reply":"pong"}}}
```

See [`../README.md`](../README.md) for the full `initialize` handshake used to exercise it by hand.
