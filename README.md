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

## Credential store (`cred`)

The `cred` package provides a generic way to access credentials from
pluggable backends. The top-level package defines the interface; each
backend lives in its own sub-package and registers a URL scheme when
imported:

```go
type Store interface {
	// Get retrieves the credential identified by key. It returns an
	// error wrapping cred.ErrNotFound when the key does not exist.
	Get(ctx context.Context, key string) (string, error)

	// Close releases any resources held by the store.
	Close() error
}
```

A store is identified by a single URL — the scheme selects the
backend and the rest of the URL locates the store. Credentials for
accessing the store itself are **never** part of the URL; each backend
takes them from its standard environment variables (for example
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, `VAULT_TOKEN`), resolved
through the backend SDK's default configuration chain.

Available backends:

| Scheme       | Sub-package     | Store URL                        |
| ------------ | --------------- | -------------------------------- |
| `json-file`  | `cred/jsonfile` | `json-file:///path/to/file.json` |

How each backend lays out credentials in its store is backend-specific
and documented per backend below.

### Usage

Import the backends you want (a blank import registers the scheme),
open the store by URL, then retrieve credentials by key:

```go
import (
	"context"
	"errors"

	"github.com/hangxie/chatops/cred"
	_ "github.com/hangxie/chatops/cred/jsonfile" // registers "json-file"
)

store, err := cred.Open(context.Background(), "json-file:///etc/chatops/creds.json")
if err != nil {
	// handle error
}
defer store.Close()

secret, err := store.Get(context.Background(), "db-password")
if errors.Is(err, cred.ErrNotFound) {
	// credential does not exist
}
```

Backends also expose a typed `Open` function for direct use, e.g.
`jsonfile.Open(ctx, "/etc/chatops/creds.json")`.

### json-file backend

The store URL is the file path (relative paths work too:
`json-file://relative/path.json`). The file must contain a single JSON
object mapping credential keys to string values:

```json
{
  "db-password": "hunter2",
  "api-token": "abc123"
}
```

### Adding a new backend

1. Create a sub-package under `cred/` named after the backend (e.g.
   `cred/vault`).
2. Define a `Store` type implementing the `cred.Store` interface:
   - `Get` returns the credential for a key, wrapping
     `cred.ErrNotFound` (with `%w`) when the key does not exist so
     callers can detect it with `errors.Is`.
   - `Close` releases connections or other resources.
3. Provide an `Open` function taking `context.Context` plus
   backend-specific location parameters and returning `(*Store, error)`.
   Take credentials for the store from the backend's standard
   environment variables (prefer the official SDK's default
   configuration chain); never accept them as parameters.
4. Register a URL scheme in `init()` so `cred.Open` can construct the
   backend from a URL:

   ```go
   func init() {
   	cred.Register("my-backend", func(ctx context.Context, u *url.URL) (cred.Store, error) {
   		return Open(ctx, u.Host+u.Path)
   	})
   }
   ```

5. Add a test file with table-driven tests covering `Open` failures,
   existing keys, missing keys, context cancellation, and opening via
   `cred.Open` with the registered scheme.
6. List the backend in the table above and document its store layout
   in a section like the json-file one.
