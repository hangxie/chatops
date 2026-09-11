package mcphost_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/mcphost"
)

func Test_New_catalog(t *testing.T) {
	var called atomic.Bool
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{
			mcphost.InProcess("alpha", "", stubServer("alpha", "one", "two")),
			mcphost.InProcess("beta", "", stubServer("beta", "three")),
		},
		HostTools: []mcphost.HostTool{hostTool("reply", &called)},
	})

	require.Equal(t, []string{"one", "reply", "three", "two"}, host.Names())

	tools := host.Tools(context.Background())
	require.Len(t, tools, 4)
	// Host tools are offered first, then each server's in connection order.
	require.Equal(t, "reply", tools[0].Name)
}

func Test_CallTool_routes_by_name(t *testing.T) {
	var called atomic.Bool
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{
			mcphost.InProcess("alpha", "", stubServer("alpha", "one")),
			mcphost.InProcess("beta", "", stubServer("beta", "two")),
		},
		HostTools: []mcphost.HostTool{hostTool("reply", &called)},
	})

	testCases := map[string]struct {
		name string
		args map[string]any
		want string
	}{
		"first-server":  {name: "one", want: "alpha/one"},
		"second-server": {name: "two", want: "beta/two"},
		"with-args":     {name: "one", args: map[string]any{"text": "hi"}, want: "alpha/one:hi"},
		"host-tool":     {name: "reply", want: "host/reply"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result, err := host.CallTool(context.Background(), tc.name, tc.args)
			require.NoError(t, err)
			require.Equal(t, tc.want, mcphost.Render(result))
		})
	}
	require.True(t, called.Load())
}

func Test_CallTool_unknown_tool(t *testing.T) {
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "one"))},
	})

	_, err := host.CallTool(context.Background(), "nope", nil)
	require.ErrorContains(t, err, `unknown tool "nope"`)
	require.ErrorContains(t, err, "offered tools: one")
}

func Test_CallTool_host_tool_error(t *testing.T) {
	host := newHost(t, mcphost.Config{
		HostTools: []mcphost.HostTool{{
			Def: &mcp.Tool{Name: "boom", Description: "Always fails."},
			Handler: func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("kaboom")
			},
		}},
	})

	_, err := host.CallTool(context.Background(), "boom", nil)
	require.ErrorContains(t, err, `call host tool "boom": kaboom`)
}

func Test_CallTool_reports_tool_error_in_result(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "alpha", Version: "v0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "fail", Description: "Always fails."},
		func(context.Context, *mcp.CallToolRequest, echoArgs) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New("tool broke")
		})
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", srv)},
	})

	// A tool reporting its own failure is not a call failure: the result
	// carries it so the requester and the model can both see it.
	result, err := host.CallTool(context.Background(), "fail", nil)
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, mcphost.Render(result), "tool broke")
}

func Test_Tools_returns_a_copy(t *testing.T) {
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "one"))},
	})

	tools := host.Tools(context.Background())
	require.Len(t, tools, 1)
	tools[0] = nil

	require.NotNil(t, host.Tools(context.Background())[0])
}

func Test_Names_is_sorted(t *testing.T) {
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "zulu", "alpha", "mike"))},
	})

	require.Equal(t, []string{"alpha", "mike", "zulu"}, host.Names())
}

func Test_CallTool_propagates_context(t *testing.T) {
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "one"))},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := host.CallTool(ctx, "one", nil)
	require.Error(t, err)
}

func Test_CallTool_host_tool_reads_context(t *testing.T) {
	type key struct{}
	var seen atomic.Value
	host := newHost(t, mcphost.Config{
		HostTools: []mcphost.HostTool{{
			Def: &mcp.Tool{Name: "peek", Description: "Report a context value."},
			Handler: func(ctx context.Context, _ map[string]any) (*mcp.CallToolResult, error) {
				value, _ := ctx.Value(key{}).(string)
				seen.Store(value)
				return &mcp.CallToolResult{}, nil
			},
		}},
	})

	// Host tools read host state from the context, which is how the reply
	// tool learns the conversation without the model naming it.
	ctx := context.WithValue(context.Background(), key{}, "conv-1")
	_, err := host.CallTool(ctx, "peek", nil)
	require.NoError(t, err)
	require.Equal(t, "conv-1", seen.Load())
}
