package cred_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/testutils"
)

func Test_Store_contract(t *testing.T) {
	var store cred.Store = testutils.CredentialStore{
		Values: map[cred.Key]string{
			cred.SlackBotToken: "xoxb-test",
			cred.PlannerAPIKey: "",
		},
	}
	defer func() {
		require.NoError(t, store.Close())
	}()

	testCases := map[string]struct {
		key      cred.Key
		expected string
		errIs    error
	}{
		"existing-key": {key: cred.SlackBotToken, expected: "xoxb-test"},
		"empty-value":  {key: cred.PlannerAPIKey, expected: ""},
		"missing-key":  {key: cred.SlackAppToken, errIs: cred.ErrNotFound},
		"unknown-key":  {key: cred.Key(255), errIs: cred.ErrNotFound},
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

func Test_Key_String(t *testing.T) {
	testCases := map[string]struct {
		key      cred.Key
		expected string
	}{
		"slack-bot":   {key: cred.SlackBotToken, expected: "slack.bot-token"},
		"slack-app":   {key: cred.SlackAppToken, expected: "slack.app-token"},
		"planner-api": {key: cred.PlannerAPIKey, expected: "planner.api-key"},
		"unknown":     {key: cred.Key(255), expected: "credential(255)"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.key.String())
		})
	}
}

func Test_Schema(t *testing.T) {
	expected := []cred.Field{
		{Key: cred.SlackBotToken, Section: "slack", Name: "bot-token"},
		{Key: cred.SlackAppToken, Section: "slack", Name: "app-token"},
		{Key: cred.PlannerAPIKey, Section: "planner", Name: "api-key"},
	}
	require.Equal(t, expected, cred.Schema())

	schema := cred.Schema()
	schema[0].Name = "changed"
	require.Equal(t, expected, cred.Schema())
}

func Test_Require(t *testing.T) {
	testErr := errors.New("store failed")
	testCases := map[string]struct {
		store    cred.Store
		expected string
		errMsg   string
		errIs    error
	}{
		"present":     {store: testutils.CredentialStore{Values: map[cred.Key]string{cred.SlackBotToken: "secret"}}, expected: "secret"},
		"nil-store":   {errMsg: "credential store is not configured", errIs: cred.ErrStoreNotConfigured},
		"missing":     {store: testutils.CredentialStore{}, errMsg: "resolve slack.bot-token", errIs: cred.ErrNotFound},
		"empty":       {store: testutils.CredentialStore{Values: map[cred.Key]string{cred.SlackBotToken: ""}}, errMsg: "credential slack.bot-token is empty"},
		"store-error": {store: testutils.CredentialStore{Err: testErr}, errMsg: "resolve slack.bot-token", errIs: testErr},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			value, err := cred.Require(context.Background(), tc.store, cred.SlackBotToken)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				if tc.errIs != nil {
					require.ErrorIs(t, err, tc.errIs)
				}
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
