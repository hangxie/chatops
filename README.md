# chatops

`chatops` connects a chat platform to an OpenAI-compatible language model and to tools served over the [Model Context Protocol](https://modelcontextprotocol.io/specification) (MCP). The model chooses which tools to call, ChatOps enforces authorization and approval, and the answer is posted back to the chat thread.

This project is under early development: the binaries currently provide only version reporting.

## Binaries

| Binary | Purpose |
|---|---|
| `chatops` | ChatOps daemon, an MCP client that connects chat, model, and MCP servers |
| `chatops-mcp` | Built-in MCP server providing first-party tools, usable by any MCP client |

## Build

Building requires Go and `make`:

```bash
make build
./build/chatops version
./build/chatops-mcp version
```

`make all` runs dependency, format, lint, test, and build steps.

## Version

Both binaries share the same `version` subcommand:

```bash
./build/chatops version --all
./build/chatops version --json --all
```

| Flag | Description |
|---|---|
| `-j`, `--json` | Output in JSON format |
| `-a`, `--all` | Output all version details |
| `-b`, `--build-time` | Output build time |
| `-s`, `--source` | Output source of the executable |

## License

BSD 3-Clause, see [LICENSE](LICENSE).
