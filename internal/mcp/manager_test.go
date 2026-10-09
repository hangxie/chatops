package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/pkg/mcpserver/ping"
)

type echoInput struct {
	Text string `json:"text"`
}

func newTestServer(name string) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "v0"}, nil)
	ping.Register(server)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in echoInput) (*mcpsdk.CallToolResult, any, error) {
		if in.Text == "fail" {
			return nil, nil, errors.New("echo failed")
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: in.Text}}}, nil, nil
	})
	return server
}

// connectTest connects a manager to each server over in-memory transports.
func connectTest(t *testing.T, servers map[string]*mcpsdk.Server) *Manager {
	t.Helper()
	transports := map[string]mcpsdk.Transport{}
	for id, server := range servers {
		serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
		session, err := server.Connect(context.Background(), serverTransport, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = session.Close() })
		transports[id] = clientTransport
	}
	manager, err := connect(context.Background(), transports, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func catalogNames(m *Manager) []string {
	names := []string{}
	for _, tool := range m.Catalog().Tools() {
		names = append(names, tool.Name)
	}
	return names
}

func Test_Manager_Catalog(t *testing.T) {
	manager := connectTest(t, map[string]*mcpsdk.Server{
		"one": newTestServer("one"),
		"two": newTestServer("two"),
	})
	require.Equal(t, []string{"one__echo", "one__ping", "two__echo", "two__ping"}, catalogNames(manager))
}

func Test_Manager_Call(t *testing.T) {
	manager := connectTest(t, map[string]*mcpsdk.Server{"one": newTestServer("one")})
	catalog := manager.Catalog()
	ping, _ := catalog.Lookup("one__ping")
	echo, _ := catalog.Lookup("one__echo")
	gone, missing := ping, ping
	gone.Server = "gone"
	missing.MCPName = "missing"

	tests := map[string]struct {
		tool   Tool
		args   string
		want   Result
		errMsg string
	}{
		"ping":          {tool: ping, args: `{}`, want: Result{Text: "pong"}},
		"ping-nil-args": {tool: ping, want: Result{Text: "pong"}},
		"ping-null":     {tool: ping, args: `null`, want: Result{Text: "pong"}},
		"invalid-type":  {tool: echo, args: `{"text":5}`, errMsg: "call one/echo: invalid arguments:"},
		"not-object":    {tool: echo, args: `["hi"]`, errMsg: "call one/echo: invalid arguments: arguments must be a JSON object"},
		"not-json":      {tool: echo, args: `{"text":`, errMsg: "call one/echo: invalid arguments: arguments are not valid JSON"},
		"no-schema": {
			tool:   Tool{Name: "one__ping", Server: "one", MCPName: "ping"},
			errMsg: "call one/ping: invalid arguments: tool one__ping has no resolved input schema",
		},
		"echo":           {tool: echo, args: `{"text":"hi"}`, want: Result{Text: "hi"}},
		"tool-error":     {tool: echo, args: `{"text":"fail"}`, want: Result{Text: "echo failed", IsError: true}},
		"unknown-server": {tool: gone, args: `{}`, errMsg: `call gone/ping: server "gone" is not connected`},
		"unknown-tool":   {tool: missing, args: `{}`, errMsg: "call one/missing:"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := manager.Call(context.Background(), tc.tool, json.RawMessage(tc.args))
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func Test_Manager_Call_sends_validated_arguments(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "raw", Version: "v0"}, nil)
	var calls int
	server.AddTool(&mcpsdk.Tool{Name: "raw", InputSchema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"mode": map[string]any{"enum": []any{"read"}}},
	}}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls++
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(req.Params.Arguments)}}}, nil
	})
	manager := connectTest(t, map[string]*mcpsdk.Server{"one": server})
	raw, ok := manager.Catalog().Lookup("one__raw")
	require.True(t, ok)

	tests := map[string]struct {
		args   string
		want   string
		errMsg string
	}{
		"big-integer-exact":  {args: `{"n":12345678901234567890}`, want: `{"n":12345678901234567890}`},
		"duplicate-last-ok":  {args: `{"mode":"write","mode":"read"}`, want: `{"mode":"read"}`},
		"duplicate-last-bad": {args: `{"mode":"read","mode":"write"}`, errMsg: "invalid arguments"},
		"empty":              {args: ``, want: `{}`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			before := calls
			got, err := manager.Call(context.Background(), raw, json.RawMessage(tc.args))
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				require.Equal(t, before, calls, "invalid arguments must not reach the server")
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tc.want, got.Text)
		})
	}
}

func Test_Manager_refreshes_on_list_changed(t *testing.T) {
	server := newTestServer("one")
	manager := connectTest(t, map[string]*mcpsdk.Server{"one": server})
	before := manager.Catalog()

	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "late"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return nil, nil, nil
	})
	require.Eventually(t, func() bool {
		_, ok := manager.Catalog().Lookup("one__late")
		return ok
	}, 5*time.Second, 10*time.Millisecond)

	// Earlier snapshots are immutable so an in-flight turn keeps its view.
	_, ok := before.Lookup("one__late")
	require.False(t, ok)

	server.RemoveTools("echo")
	require.Eventually(t, func() bool {
		_, ok := manager.Catalog().Lookup("one__echo")
		return !ok
	}, 5*time.Second, 10*time.Millisecond)
}

func Test_Manager_logs_skipped_tools(t *testing.T) {
	server := newTestServer("one")
	server.AddTool(&mcpsdk.Tool{Name: "remote-ref", InputSchema: map[string]any{"type": "object", "$ref": "https://example.com/s.json"}},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{}, nil
		})

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	session, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	var logs bytes.Buffer
	manager, err := connect(context.Background(), map[string]mcpsdk.Transport{"one": clientTransport}, slog.New(slog.NewTextHandler(&logs, nil)))
	require.NoError(t, err)
	defer func() { _ = manager.Close() }()

	require.Equal(t, []string{"one__echo", "one__ping"}, catalogNames(manager))
	require.Contains(t, logs.String(), "skip mcp tool")
	require.Contains(t, logs.String(), "one/remote-ref")
}

func Test_Manager_Close(t *testing.T) {
	manager := connectTest(t, map[string]*mcpsdk.Server{"one": newTestServer("one")})
	ping, _ := manager.Catalog().Lookup("one__ping")
	require.NoError(t, manager.Close())
	require.NoError(t, manager.Close())

	_, err := manager.Call(context.Background(), ping, nil)
	require.ErrorContains(t, err, `server "one" is not connected`)
}

func Test_Open(t *testing.T) {
	manager, err := Open(context.Background(), map[string]config.Server{"local": stdioTestServer()}, nil)
	require.NoError(t, err)
	defer func() { _ = manager.Close() }()

	require.Equal(t, []string{"local__ping"}, catalogNames(manager))
	ping, _ := manager.Catalog().Lookup("local__ping")
	got, err := manager.Call(context.Background(), ping, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Equal(t, Result{Text: "pong"}, got)
}

func Test_Open_no_servers(t *testing.T) {
	manager, err := Open(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Empty(t, manager.Catalog().Tools())
	require.NoError(t, manager.Close())
}

func Test_Open_errors(t *testing.T) {
	tests := map[string]struct {
		servers map[string]config.Server
		errMsg  string
	}{
		"bad-transport": {
			servers: map[string]config.Server{"x": {Transport: "sse"}},
			errMsg:  `mcp server "x": unsupported transport "sse"`,
		},
		"connect-fails": {
			servers: map[string]config.Server{
				"good": stdioTestServer(),
				"bad":  {Transport: config.TransportStdio, Command: "/nonexistent/chatops-mcp"},
			},
			errMsg: `mcp server "bad": connect:`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			manager, err := Open(context.Background(), tc.servers, nil)
			require.ErrorContains(t, err, tc.errMsg)
			require.Nil(t, manager)
		})
	}
}
