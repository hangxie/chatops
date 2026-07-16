package jsonfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func Test_Open(t *testing.T) {
	testCases := map[string]struct {
		content string
		errMsg  string
	}{
		"valid":            {content: `{"db-password": "hunter2", "api-token": "abc123"}`},
		"empty-object":     {content: `{}`},
		"invalid-json":     {content: `{not json`, errMsg: "parse"},
		"non-string-value": {content: `{"db": {"user": "app"}}`, errMsg: "parse"},
		"top-level-array":  {content: `["a", "b"]`, errMsg: "parse"},
		"top-level-null":   {content: `null`, errMsg: "parse"},
		"null-value":       {content: `{"token": null}`, errMsg: `credential "token" is null`},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			store, err := Open(context.Background(), writeFile(t, tc.content))
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.NoError(t, store.Close())
		})
	}
}

func Test_Open_missing_file(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "no-such-file.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func Test_Open_cancelled_context(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, writeFile(t, `{}`))
	require.ErrorIs(t, err, context.Canceled)
}

func Test_Open_via_registered_scheme(t *testing.T) {
	path := writeFile(t, `{"db-password": "hunter2"}`)

	store, err := cred.Open(context.Background(), "json-file://"+path)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	value, err := store.Get(context.Background(), "db-password")
	require.NoError(t, err)
	require.Equal(t, "hunter2", value)
}

func Test_Open_via_registered_scheme_relative_path(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "creds.json"), []byte(`{"api-token": "abc123"}`), 0o600))
	t.Chdir(dir)

	store, err := cred.Open(context.Background(), "json-file://creds.json")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	value, err := store.Get(context.Background(), "api-token")
	require.NoError(t, err)
	require.Equal(t, "abc123", value)
}

func Test_Store_implements_cred_Store(t *testing.T) {
	var _ cred.Store = (*Store)(nil)
}

func Test_Get(t *testing.T) {
	store, err := Open(context.Background(), writeFile(t, `{"db-password": "hunter2", "empty": ""}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	testCases := map[string]struct {
		key      string
		expected string
		errIs    error
	}{
		"existing-key": {key: "db-password", expected: "hunter2"},
		"empty-value":  {key: "empty", expected: ""},
		"missing-key":  {key: "no-such-key", errIs: cred.ErrNotFound},
		"empty-key":    {key: "", errIs: cred.ErrNotFound},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			value, err := store.Get(context.Background(), tc.key)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, value)
		})
	}
}

func Test_Get_cancelled_context(t *testing.T) {
	store, err := Open(context.Background(), writeFile(t, `{"db-password": "hunter2"}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Get(ctx, "db-password")
	require.ErrorIs(t, err, context.Canceled)
}
