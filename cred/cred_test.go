package cred_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
)

// fakeStore is a minimal in-memory cred.Store used to exercise the
// interface contract the way a real backend is expected to behave.
type fakeStore struct {
	creds map[string]string
}

func (f *fakeStore) Get(_ context.Context, key string) (string, error) {
	value, ok := f.creds[key]
	if !ok {
		return "", fmt.Errorf("%q: %w", key, cred.ErrNotFound)
	}
	return value, nil
}

func (f *fakeStore) Close() error {
	return nil
}

func Test_Store_contract(t *testing.T) {
	var store cred.Store = &fakeStore{
		creds: map[string]string{
			"db-password": "hunter2",
			"empty":       "",
		},
	}
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

func Test_ErrNotFound_is_stable_sentinel(t *testing.T) {
	require.EqualError(t, cred.ErrNotFound, "credential not found")
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", cred.ErrNotFound), cred.ErrNotFound))
}
