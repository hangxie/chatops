// Package jsonfile implements a cred.Store backed by a JSON file.
//
// Importing this package registers the "json-file" URL scheme with
// cred.Open; the rest of the URL is the file path:
//
//	json-file:///etc/chatops/creds.json
//	json-file://relative/path/creds.json
//
// The file must contain a single JSON object mapping credential keys
// to string values:
//
//	{
//	  "db-password": "hunter2",
//	  "api-token": "abc123"
//	}
package jsonfile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/hangxie/chatops/cred"
)

func init() {
	cred.Register("json-file", func(ctx context.Context, u *url.URL) (cred.Store, error) {
		// Rejoin host and path so both json-file:///abs/path and
		// json-file://relative/path resolve to a usable file path.
		return Open(ctx, u.Host+u.Path)
	})
}

// Store is a cred.Store backed by a JSON file. The file is read once
// by Open; later changes to the file are not observed.
type Store struct {
	creds map[string]string
}

// Open reads and parses the JSON file at path and connects it as a
// credential store.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("jsonfile: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jsonfile: %w", err)
	}
	// Decode through *string so JSON null is distinguishable from ""
	// instead of silently becoming an empty credential; a top-level
	// null would likewise leave the map nil without an error.
	var raw map[string]*string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("jsonfile: parse %s: %w", path, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("jsonfile: parse %s: top-level value must be a JSON object", path)
	}
	creds := make(map[string]string, len(raw))
	for key, value := range raw {
		if value == nil {
			return nil, fmt.Errorf("jsonfile: parse %s: credential %q is null", path, key)
		}
		creds[key] = *value
	}
	return &Store{creds: creds}, nil
}

// Get retrieves the credential identified by key.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("jsonfile: %w", err)
	}
	value, ok := s.creds[key]
	if !ok {
		return "", fmt.Errorf("jsonfile: %q: %w", key, cred.ErrNotFound)
	}
	return value, nil
}

// Close releases resources held by the store; it is a no-op for a
// JSON file since the file is not kept open.
func (s *Store) Close() error {
	return nil
}
