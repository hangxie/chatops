// Package cred provides a generic interface for accessing credentials
// from pluggable credential store backends.
//
// Each backend lives in its own sub-package and registers a URL
// scheme when imported, so a store can be opened from a single URL:
//
//	import _ "github.com/hangxie/chatops/cred/jsonfile"
//
//	store, err := cred.Open(ctx, "json-file:///etc/chatops/creds.json")
//
// Backends also expose a typed Open function for direct programmatic
// use. Credentials for accessing a store itself are never part of the
// URL; backends take them from their standard environment variables
// (e.g. AWS_ACCESS_KEY_ID, VAULT_TOKEN).
package cred

import (
	"context"
	"errors"
)

// ErrNotFound is the sentinel error reported by Store.Get when the
// requested credential does not exist. Backends wrap it with context,
// so check for it with errors.Is.
var ErrNotFound = errors.New("credential not found")

// Store is a handle to an opened credential store.
//
// Implementations must be safe for concurrent use by multiple
// goroutines, except that Close must not be called concurrently with
// Get.
type Store interface {
	// Get retrieves the credential identified by key. It returns an
	// error wrapping ErrNotFound when the key does not exist.
	Get(ctx context.Context, key string) (string, error)

	// Close releases any resources held by the store. Calling Get
	// after Close is invalid.
	Close() error
}
