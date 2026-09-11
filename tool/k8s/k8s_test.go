package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/mcpserve"
)

func Test_Register_options(t *testing.T) {
	path := writeKubeconfig(t)

	testCases := map[string]struct {
		opts   url.Values
		errMsg string
	}{
		"none":                 {opts: nil},
		"kubeconfig":           {opts: url.Values{optionKubeconfig: {path}}},
		"context":              {opts: url.Values{optionKubeconfig: {path}, optionContext: {"alpha"}}},
		"unresolvable-cluster": {opts: url.Values{optionContext: {"no-such-context"}}},
		"unknown-key":          {opts: url.Values{"cluster": {"prod"}}, errMsg: `unknown option cluster`},
		"namespace":            {opts: url.Values{"namespace": {"web"}}, errMsg: `unknown option namespace`},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
			err := Register(srv, mcpserve.Options{Query: tc.opts})
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
		})
	}
}

// Test_Register_defers_the_kubeconfig checks that a cluster which cannot be
// resolved does not stop the group from being served. The group is served by
// default, so a host with no kubeconfig — CI, or any machine that never talks
// to Kubernetes — must still start; the problem surfaces on the call instead.
func Test_Register_defers_the_kubeconfig(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, Register(srv, mcpserve.Options{Query: url.Values{
		optionKubeconfig: {filepath.Join(t.TempDir(), "missing.yaml")},
		optionContext:    {"nope"},
	}}))
	session := testutils.MCPSession(t, srv)

	require.Equal(t, []string{GetToolName, ListToolName}, testutils.ToolNames(t, session))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      ListToolName,
		Arguments: map[string]any{"kind": "pods"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	// The kubeconfig path stays out of the requester's view.
	text := testutils.ResultText(t, result)
	require.Contains(t, text, "could not be reached")
	require.NotContains(t, text, "missing.yaml")
}

func Test_RegisterClient_declares_tools(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, &fakeClient{}, nil))
	session := testutils.MCPSession(t, srv)

	require.Equal(t, []string{GetToolName, ListToolName}, testutils.ToolNames(t, session))
}

// Test_tools_typed_arguments checks that the declared schema types
// all-namespaces as a real boolean: the server accepts a JSON boolean and
// rejects a string, so the tools no longer parse booleans out of text.
func Test_tools_typed_arguments(t *testing.T) {
	var gotAll bool
	client := &fakeClient{
		listFn: func(_ context.Context, _, _ string, all bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
			gotAll = all
			return &unstructured.UnstructuredList{}, podMapping(t), nil
		},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, client, nil))
	session := testutils.MCPSession(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      ListToolName,
		Arguments: map[string]any{"kind": "pods", "all-namespaces": true},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.True(t, gotAll)

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      ListToolName,
		Arguments: map[string]any{"kind": "pods", "all-namespaces": "maybe"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), "all-namespaces")
}

// Test_cluster_failures_are_curated checks that what reaches the requester
// says what failed without saying where. A tool result is relayed into chat,
// and client-go errors carry API server addresses and the identity an
// authorization check rejected.
func Test_cluster_failures_are_curated(t *testing.T) {
	var logs bytes.Buffer
	leaky := errors.New(`Get "https://10.1.2.3:6443/api/v1/pods": pods is forbidden: User "system:serviceaccount:ops:chatops" cannot list resource`)
	client := &fakeClient{
		listFn: func(context.Context, string, string, bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
			return nil, nil, leaky
		},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, client, slog.New(slog.NewTextHandler(&logs, nil))))
	session := testutils.MCPSession(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      ListToolName,
		Arguments: map[string]any{"kind": "pods"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)

	text := testutils.ResultText(t, result)
	require.NotContains(t, text, "10.1.2.3")
	require.NotContains(t, text, "serviceaccount")
	require.Contains(t, text, "could not be reached")

	// The detail is not lost, it is logged.
	require.Contains(t, logs.String(), "10.1.2.3")
	require.Contains(t, logs.String(), "tool=k8s-list")
}

// Test_refusal_is_reported_as_a_refusal: being told the cluster might be
// unreachable, when the real answer is that this bot may not read the
// resource, sends someone to look in the wrong place.
func Test_refusal_is_reported_as_a_refusal(t *testing.T) {
	var logs bytes.Buffer
	refused := apierrors.NewForbidden(
		schema.GroupResource{Resource: "secrets"}, "db",
		errors.New(`User "system:serviceaccount:ops:chatops" cannot get resource "secrets"`),
	)
	client := &fakeClient{
		listFn: func(context.Context, string, string, bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
			return nil, nil, refused
		},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, client, slog.New(slog.NewTextHandler(&logs, nil))))
	session := testutils.MCPSession(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      ListToolName,
		Arguments: map[string]any{"kind": "secrets"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)

	text := testutils.ResultText(t, result)
	require.Contains(t, text, "not permitted")
	require.NotContains(t, text, "could not be reached")
	// The identity that was refused stays in the log.
	require.NotContains(t, text, "serviceaccount")
	require.Contains(t, logs.String(), "serviceaccount")
	require.Contains(t, logs.String(), "kubernetes call refused")
}

func Test_curate_separates_cancellation_from_a_timeout(t *testing.T) {
	// Whether the caller went away is decided by the context, not by the
	// error: an http.Client timeout reports an error matching
	// context.DeadlineExceeded that carries the address it was calling.
	leakyTimeout := fmt.Errorf(`Get "https://10.1.2.3:6443/api": %w (Client.Timeout exceeded)`, context.DeadlineExceeded)

	t.Run("caller went away", func(t *testing.T) {
		var logs bytes.Buffer
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()

		err := curate(cancelled, slog.New(slog.NewTextHandler(&logs, nil)), ListToolName, context.Canceled)

		// Ordinary during shutdown, so not logged as a failure on every
		// restart, and it says nothing beyond that.
		require.EqualError(t, err, "k8s: call cancelled")
		require.Empty(t, logs.String())
	})

	t.Run("timed out inside the call", func(t *testing.T) {
		var logs bytes.Buffer

		err := curate(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), ListToolName, leakyTimeout)

		// An operational failure like any other: hidden and logged.
		require.NotContains(t, err.Error(), "10.1.2.3")
		require.Contains(t, err.Error(), "could not be reached")
		require.Contains(t, logs.String(), "10.1.2.3")
	})
}

func Test_invalidCall_preserves_the_wrapped_error(t *testing.T) {
	// Marking an error as safe to relay must not hide what it wraps, so a
	// sentinel underneath still matches.
	sentinel := errors.New("underlying")
	marked := invalidCall("k8s: bad argument: %w", sentinel)

	require.ErrorIs(t, marked, sentinel)
	require.Equal(t, "k8s: bad argument: underlying", marked.Error())

	// And it is still recognised as relayable, so curate passes it through.
	require.Equal(t, marked, curate(context.Background(), slog.New(slog.DiscardHandler), ListToolName, marked))
}

// Test_invalid_call_errors_reach_the_requester: the argument the caller got
// wrong is the whole point of reporting it, so those pass through uncurated.
func Test_invalid_call_errors_reach_the_requester(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, &fakeClient{}, slog.New(slog.DiscardHandler)))
	session := testutils.MCPSession(t, srv)

	testCases := map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"missing kind": {tool: ListToolName, args: map[string]any{"kind": " "}, want: "requires a kind"},
		"missing name": {tool: GetToolName, args: map[string]any{"kind": "pod", "name": " "}, want: "requires a name"},
		"bad output": {
			tool: GetToolName,
			args: map[string]any{"kind": "pod", "name": "api", "output": "toml"},
			want: "unknown output",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Contains(t, testutils.ResultText(t, result), tc.want)
		})
	}
}

func Test_get_tool_returns_a_resource(t *testing.T) {
	client := &fakeClient{
		getFn: func(_ context.Context, _, _, name string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
			return newPod("web", name), nil, nil
		},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, client, nil))
	session := testutils.MCPSession(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      GetToolName,
		Arguments: map[string]any{"kind": "pod", "name": "api", "output": "json"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), `"kind": "Pod"`)
}

func Test_get_tool_requires_kind_and_name(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, &fakeClient{}, nil))
	session := testutils.MCPSession(t, srv)

	// Both are required properties, so the server rejects the call against
	// the declared schema before the handler runs.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      GetToolName,
		Arguments: map[string]any{"kind": "pod"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), "name")
}
