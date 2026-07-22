package testutils

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
)

func Test_CredentialStore(t *testing.T) {
	testErr := errors.New("store failed")
	testCases := map[string]struct {
		store    CredentialStore
		key      cred.Key
		expected string
		errIs    error
	}{
		"present": {store: CredentialStore{Values: map[cred.Key]string{cred.SlackBotToken: "secret"}}, key: cred.SlackBotToken, expected: "secret"},
		"empty":   {store: CredentialStore{Values: map[cred.Key]string{cred.SlackBotToken: ""}}, key: cred.SlackBotToken},
		"missing": {store: CredentialStore{}, key: cred.SlackBotToken, errIs: cred.ErrNotFound},
		"error":   {store: CredentialStore{Err: testErr}, key: cred.SlackBotToken, errIs: testErr},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			actual, err := tc.store.Get(context.Background(), tc.key)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, actual)
			require.NoError(t, tc.store.Close())
		})
	}
}
