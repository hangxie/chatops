// Package jsonfile implements a cred.Store backed by a JSON file.
//
// The package exports Scheme and Opener for wiring the backend into a
// cred.Registry under the "json-file" URL scheme; the rest of the URL
// is the file path:
//
//	json-file:///etc/chatops/creds.json
//	json-file://relative/path/creds.json
//	json-file://~/creds.json
//
// A leading "~" (or "~/") in the path expands to the current user's home
// directory, since a shell never expands a "~" embedded in a URL.
//
// The file uses a strict schema with optional Slack and planner sections:
//
//	{
//	  "slack": {
//	    "bot-token": "xoxb-...",
//	    "app-token": "xapp-..."
//	  },
//	  "planner": {"api-key": "sk-..."}
//	}
package jsonfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/hangxie/chatops/cred"
)

// Scheme is the URL scheme this backend serves in a cred.Registry.
const Scheme = "json-file"

// Opener is the cred.OpenerFunc for this backend: the URL locates the
// JSON file.
func Opener(ctx context.Context, u *url.URL) (cred.Store, error) {
	// Rejoin host and path so both json-file:///abs/path and
	// json-file://relative/path resolve to a usable file path; fall back to
	// the opaque form (json-file:path) so it is not silently dropped.
	path := u.Host + u.Path
	if path == "" {
		path = u.Opaque
	}
	return Open(ctx, path)
}

// Store is a cred.Store backed by a JSON file. The file is read once
// by Open; later changes to the file are not observed.
type Store struct {
	creds map[cred.Key]string
}

type objectField struct {
	name  string
	value json.RawMessage
}

// Open reads and parses the JSON file at path and connects it as a
// credential store.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("jsonfile: %w", err)
	}
	path, err := expandHome(path)
	if err != nil {
		return nil, fmt.Errorf("jsonfile: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jsonfile: %w", err)
	}
	creds, err := decodeCredentials(data)
	if err != nil {
		return nil, fmt.Errorf("jsonfile: parse %s: %w", path, err)
	}
	return &Store{creds: creds}, nil
}

// expandHome expands a leading "~" or "~/" in path to the current user's
// home directory. A "~" embedded in a URL is never expanded by the shell,
// so the backend does it. Other forms (such as "~user") are left unchanged.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[len("~/"):]), nil
}

func decodeCredentials(data []byte) (map[cred.Key]string, error) {
	sections, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	schema := schemaBySection()
	creds := make(map[cred.Key]string, 3)
	for _, section := range sections {
		fields, ok := schema[section.name]
		if !ok {
			return nil, fmt.Errorf("unknown field %q", section.name)
		}
		if err := decodeCredentialSection(creds, section.name, fields, section.value); err != nil {
			return nil, err
		}
	}
	return creds, nil
}

func schemaBySection() map[string]map[string]cred.Key {
	sections := make(map[string]map[string]cred.Key)
	for _, field := range cred.Schema() {
		if sections[field.Section] == nil {
			sections[field.Section] = make(map[string]cred.Key)
		}
		sections[field.Section][field.Name] = field.Key
	}
	return sections
}

func decodeCredentialSection(creds map[cred.Key]string, section string, schema map[string]cred.Key, data json.RawMessage) error {
	fields, err := decodeObject(data)
	if err != nil {
		return fmt.Errorf("%s: %w", section, err)
	}
	for _, field := range fields {
		key, ok := schema[field.name]
		if !ok {
			return fmt.Errorf("%s: unknown field %q", section, field.name)
		}
		if err := setCredential(creds, key, field.value); err != nil {
			return err
		}
	}
	return nil
}

func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func decodeObject(data []byte) ([]objectField, error) {
	if !isJSONObject(data) {
		return nil, errors.New("must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	_, _ = decoder.Token() // isJSONObject established the opening delimiter.
	var fields []objectField
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name := token.(string)
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields = append(fields, objectField{name: name, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if err == nil {
		return nil, errors.New("multiple JSON values")
	}
	if err != io.EOF {
		return nil, err
	}
	return fields, nil
}

func setCredential(creds map[cred.Key]string, key cred.Key, data json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("%s must be a string", key)
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("%s must be a string", key)
	}
	creds[key] = value
	return nil
}

// Get retrieves the credential identified by key.
func (s *Store) Get(ctx context.Context, key cred.Key) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("jsonfile: %w", err)
	}
	value, ok := s.creds[key]
	if !ok {
		return "", fmt.Errorf("jsonfile: %s: %w", key, cred.ErrNotFound)
	}
	return value, nil
}

// Close releases resources held by the store; it is a no-op for a
// JSON file since the file is not kept open.
func (s *Store) Close() error {
	return nil
}
