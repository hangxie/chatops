package testutils

import (
	"context"
	"fmt"

	"github.com/hangxie/chatops/cred"
)

// CredentialStore is a map-backed cred.Store for tests.
type CredentialStore struct {
	Values map[cred.Key]string
	Err    error
}

// Get returns the configured value or error.
func (s CredentialStore) Get(_ context.Context, key cred.Key) (string, error) {
	if s.Err != nil {
		return "", s.Err
	}
	value, ok := s.Values[key]
	if !ok {
		return "", fmt.Errorf("%s: %w", key, cred.ErrNotFound)
	}
	return value, nil
}

// Close releases no resources.
func (CredentialStore) Close() error { return nil }
