# chatops

## Usage

```bash
$ chatops --help
Usage: chatops <command>

Commands:
  version    Show build version.
```

### version

Prints build version, optionally with build time and source, in plain text or JSON:

```bash
$ chatops version
v0.1.0

$ chatops version --all --json
{"Version":"v0.1.0","BuildTime":"2026-07-15T22:04:35-0700","Source":"github"}
```
