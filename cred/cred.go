// Package cred provides a generic interface for accessing credentials
// from pluggable credential store backends.
//
// Each backend lives in its own sub-package and exports its URL
// scheme and opener; callers wire the backends they support into a
// Registry, so a store can be opened from a single URL:
//
//	reg := cred.NewRegistry(
//		cred.Backend{Scheme: jsonfile.Scheme, Opener: jsonfile.Opener},
//	)
//	store, err := reg.Open(ctx, "json-file:///etc/chatops/creds.json")
//
// Backends also expose a typed Open function for direct programmatic
// use. Credentials for accessing a store itself are never part of the
// URL; backends take them from their standard environment variables
// (e.g. AWS_ACCESS_KEY_ID, VAULT_TOKEN).
package cred

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotFound is the sentinel error reported by Store.Get when the
// requested credential does not exist. Backends wrap it with context,
// so check for it with errors.Is.
var ErrNotFound = errors.New("credential not found")

// ErrStoreNotConfigured reports that an operation requiring credentials
// received no credential store.
var ErrStoreNotConfigured = errors.New("credential store is not configured")

// Key identifies one credential in the application schema. Add new keys and
// their paths to the schema table below.
type Key uint8

const (
	// SlackBotToken is the Slack bot OAuth token.
	SlackBotToken Key = iota + 1
	// SlackAppToken is the Slack Socket Mode app token.
	SlackAppToken
	// PlannerAPIKey authenticates the configured planner endpoint.
	PlannerAPIKey
)

// Field describes one credential in the application schema.
type Field struct {
	Key     Key
	Section string
	Name    string
}

var schema = [...]Field{
	{Key: SlackBotToken, Section: "slack", Name: "bot-token"},
	{Key: SlackAppToken, Section: "slack", Name: "app-token"},
	{Key: PlannerAPIKey, Section: "planner", Name: "api-key"},
}

// Schema returns a copy of the predefined credential schema.
func Schema() []Field {
	return append([]Field(nil), schema[:]...)
}

// String returns the stable, human-readable schema path for a key.
func (k Key) String() string {
	for _, field := range schema {
		if field.Key == k {
			return field.Section + "." + field.Name
		}
	}
	return fmt.Sprintf("credential(%d)", k)
}

// Store is a handle to an opened credential store.
//
// Implementations must be safe for concurrent use by multiple
// goroutines, except that Close must not be called concurrently with
// Get.
type Store interface {
	// Get retrieves the credential identified by the predefined key. It returns
	// an error wrapping ErrNotFound when the key does not exist.
	Get(ctx context.Context, key Key) (string, error)

	// Close releases any resources held by the store. Calling Get
	// after Close is invalid.
	Close() error
}

// Require retrieves a non-empty credential from store.
func Require(ctx context.Context, store Store, key Key) (string, error) {
	if store == nil {
		return "", ErrStoreNotConfigured
	}
	value, err := store.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", key, err)
	}
	if value == "" {
		return "", fmt.Errorf("credential %s is empty", key)
	}
	return value, nil
}
