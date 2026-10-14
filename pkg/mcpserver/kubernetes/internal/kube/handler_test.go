package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func Test_ReadOnly(t *testing.T) {
	a := ReadOnly()
	require.True(t, a.ReadOnlyHint)
	require.True(t, a.IdempotentHint)
	require.True(t, *a.OpenWorldHint)
}

func Test_TextResult(t *testing.T) {
	require.Equal(t, "hi", TextResult("hi").Content[0].(*mcp.TextContent).Text)
}

type upperInput struct{ Names []string }

type upperOutput struct{ Names []string }

func Test_PerTarget(t *testing.T) {
	handler := PerTarget(
		func(in upperInput) ([]string, error) { return Targets("names", in.Names) },
		func(_ context.Context, name string) (string, error) {
			if name == "bad" {
				return "", errors.New("boom")
			}
			return strings.ToUpper(name), nil
		},
		func(s string) string { return "[" + s + "]" },
		func(items []string) upperOutput { return upperOutput{Names: items} },
	)
	tests := map[string]struct {
		names  []string
		text   string
		out    upperOutput
		errMsg string
	}{
		"renders each in order": {names: []string{"a", "b", "a"}, text: "[A]\n\n[B]", out: upperOutput{Names: []string{"A", "B"}}},
		"invalid targets":       {names: nil, errMsg: "names must contain at least one target"},
		"fetch failure":         {names: []string{"a", "bad"}, errMsg: "bad: boom"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, out, err := handler(context.Background(), nil, upperInput{Names: tc.names})
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.text, result.Content[0].(*mcp.TextContent).Text)
			require.Equal(t, tc.out, out)
		})
	}
}
