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
		"valid": {content: `{
			"slack":{"bot-token":"xoxb-test","app-token":"xapp-test"},
			"planner":{"api-key":"sk-test"}
		}`},
		"empty-object":          {content: `{}`},
		"empty-sections":        {content: `{"slack":{},"planner":{}}`},
		"invalid-json":          {content: `{not json`, errMsg: "parse"},
		"multiple-values":       {content: `{} {}`, errMsg: "multiple JSON values"},
		"top-level-array":       {content: `["a", "b"]`, errMsg: "object"},
		"top-level-null":        {content: `null`, errMsg: "object"},
		"unknown-section":       {content: `{"database":{}}`, errMsg: `unknown field "database"`},
		"section-case":          {content: `{"Slack":{}}`, errMsg: `unknown field "Slack"`},
		"unknown-slack-field":   {content: `{"slack":{"token":"secret"}}`, errMsg: `unknown field "token"`},
		"unknown-planner-field": {content: `{"planner":{"token":"secret"}}`, errMsg: `unknown field "token"`},
		"duplicate-section":     {content: `{"planner":{"api-key":"first"},"planner":{"api-key":"second"}}`, errMsg: `duplicate field "planner"`},
		"duplicate-slack-field": {content: `{"slack":{"bot-token":"first","bot-token":"second"}}`, errMsg: `duplicate field "bot-token"`},
		"duplicate-planner-key": {content: `{"planner":{"api-key":"first","api-key":"second"}}`, errMsg: `duplicate field "api-key"`},
		"slack-field-case":      {content: `{"slack":{"Bot-Token":"secret"}}`, errMsg: `unknown field "Bot-Token"`},
		"planner-field-case":    {content: `{"planner":{"API-Key":"secret"}}`, errMsg: `unknown field "API-Key"`},
		"null-section":          {content: `{"slack":null}`, errMsg: "must be an object"},
		"array-section":         {content: `{"slack":[]}`, errMsg: "must be an object"},
		"null-bot-token":        {content: `{"slack":{"bot-token":null}}`, errMsg: "slack.bot-token must be a string"},
		"invalid-app-token":     {content: `{"slack":{"app-token":123}}`, errMsg: "slack.app-token must be a string"},
		"invalid-planner-key":   {content: `{"planner":{"api-key":true}}`, errMsg: "planner.api-key must be a string"},
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

func Test_Open_sample_file(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join("..", "..", "scripts", "cred-store-sample.json"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	for _, field := range cred.Schema() {
		value, err := store.Get(context.Background(), field.Key)
		require.NoError(t, err)
		require.NotEmpty(t, value)
	}
}

func Test_Open_missing_file(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "no-such-file.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func Test_decodeObject_rejects_malformed_objects(t *testing.T) {
	testCases := map[string]string{
		"missing-value":    `{"key":}`,
		"missing-close":    `{"key":"value"`,
		"invalid-trailing": `{} trailing`,
	}
	for name, content := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeObject([]byte(content))
			require.Error(t, err)
		})
	}
}

func Test_Open_cancelled_context(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, writeFile(t, `{}`))
	require.ErrorIs(t, err, context.Canceled)
}

// testRegistry wires the backend into a cred.Registry the way a
// caller is expected to.
func testRegistry() *cred.Registry {
	return cred.NewRegistry(cred.Backend{Scheme: Scheme, Opener: Opener})
}

func Test_Open_via_registry(t *testing.T) {
	path := writeFile(t, `{"slack":{"bot-token":"xoxb-test"}}`)

	store, err := testRegistry().Open(context.Background(), "json-file://"+path)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	value, err := store.Get(context.Background(), cred.SlackBotToken)
	require.NoError(t, err)
	require.Equal(t, "xoxb-test", value)
}

func Test_Open_via_registry_relative_path(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "creds.json"), []byte(`{"planner":{"api-key":"sk-test"}}`), 0o600))
	t.Chdir(dir)

	store, err := testRegistry().Open(context.Background(), "json-file://creds.json")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	value, err := store.Get(context.Background(), cred.PlannerAPIKey)
	require.NoError(t, err)
	require.Equal(t, "sk-test", value)
}

func Test_Store_implements_cred_Store(t *testing.T) {
	var _ cred.Store = (*Store)(nil)
}

func Test_Get(t *testing.T) {
	store, err := Open(context.Background(), writeFile(t, `{
		"slack":{"bot-token":"xoxb-test"},
		"planner":{"api-key":""}
	}`))
	require.NoError(t, err)
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

func Test_Get_cancelled_context(t *testing.T) {
	store, err := Open(context.Background(), writeFile(t, `{"slack":{"bot-token":"xoxb-test"}}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Get(ctx, cred.SlackBotToken)
	require.ErrorIs(t, err, context.Canceled)
}
