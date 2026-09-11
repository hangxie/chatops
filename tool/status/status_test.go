package status

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/mcpserve"
)

// newSession serves the status tools backed by checker and connects a client
// to them, so every call in these tests crosses the real protocol boundary.
func newSession(t *testing.T, checker *Checker) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterChecker(srv, checker, nil))
	return testutils.MCPSession(t, srv)
}

// call invokes one status tool through the session.
func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return result
}

func Test_check_tool(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{
		name: "github",
		snap: Snapshot{Health: HealthDegraded, Summary: "Degraded Performance", Incidents: []Incident{{Name: "API errors", Status: "monitoring", URL: "https://example.test/incident"}}},
	}})
	require.NoError(t, err)
	session := newSession(t, checker)

	testCases := map[string]struct {
		args    map[string]any
		text    string
		health  map[string]any
		errText string
	}{
		"check": {
			args:   map[string]any{"service": "github"},
			text:   "[DEGRADED] GitHub — Degraded Performance\n  API errors (monitoring)\n  https://example.test/incident",
			health: map[string]any{"github": "degraded"},
		},
		"blank-service":   {args: map[string]any{"service": "  "}, errText: "requires a service"},
		"unknown-service": {args: map[string]any{"service": "missing"}, errText: "unknown service-status provider"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := call(t, session, CheckToolName, tc.args)
			if tc.errText != "" {
				// A tool reporting its own failure does so in the result, so
				// the requester and the model can both see what went wrong.
				require.True(t, result.IsError)
				require.Contains(t, testutils.ResultText(t, result), tc.errText)
				return
			}
			require.False(t, result.IsError)
			require.Equal(t, tc.text, testutils.ResultText(t, result))
			require.Equal(t, tc.health, result.StructuredContent)
		})
	}
}

func Test_check_tool_requires_service(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)
	session := newSession(t, checker)

	// "service" is a required property, so the server rejects the call
	// against the declared schema before the handler runs.
	result := call(t, session, CheckToolName, map[string]any{})
	require.True(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), "service")
}

func Test_list_tool(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)
	session := newSession(t, checker)

	result := call(t, session, ListToolName, nil)
	require.False(t, result.IsError)
	require.Equal(t, "Supported services: github", testutils.ResultText(t, result))
	require.Equal(t, []any{"github"}, result.StructuredContent)
}

func Test_Register_options(t *testing.T) {
	testCases := map[string]struct {
		opts   url.Values
		errMsg string
	}{
		"none":    {opts: nil},
		"unknown": {opts: url.Values{"timeout": {"1"}}, errMsg: "takes no options"},
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

// Test_RegisterChecker_reports_through_the_given_logger: a caller wiring a
// status server directly gets the same reporting the group does, rather than
// having its failure reasons discarded.
func Test_RegisterChecker_reports_through_the_given_logger(t *testing.T) {
	var logs bytes.Buffer
	shared, err := NewChecker([]Provider{fakeProvider{name: "github", err: errors.New("proxyconnect tcp 10.9.9.9:3128")}})
	require.NoError(t, err)

	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterChecker(srv, shared, slog.New(slog.NewTextHandler(&logs, nil))))
	session := testutils.MCPSession(t, srv)

	result := call(t, session, CheckToolName, map[string]any{"service": "github"})
	require.False(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), "Unable to check")
	require.NotContains(t, testutils.ResultText(t, result), "10.9.9.9")
	require.Contains(t, logs.String(), "10.9.9.9")

	// The catalog it was given is untouched.
	require.Nil(t, shared.logger)
}

func Test_Register_rejects_nil_checker(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.ErrorIs(t, RegisterChecker(srv, nil, nil), ErrNilChecker)
}

func Test_Register_uses_default_catalog(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, Register(srv, mcpserve.Options{}))
	session := testutils.MCPSession(t, srv)

	require.Equal(t, []string{CheckToolName, ListToolName}, testutils.ToolNames(t, session))

	result := call(t, session, ListToolName, nil)
	require.False(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), "github")
}

func Test_call_cancelled_context(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)
	session := newSession(t, checker)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = session.CallTool(ctx, &mcp.CallToolParams{Name: ListToolName})
	require.Error(t, err)
}

func Test_handlers_honor_cancellation(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Each handler guards its own context, so a call cancelled after it has
	// been dispatched stops rather than reaching the network.
	_, _, err = checkStatus(ctx, checker, CheckArgs{Service: "github"})
	require.ErrorIs(t, err, context.Canceled)

	_, _, err = listServices(ctx, checker)
	require.ErrorIs(t, err, context.Canceled)
}

func Test_healthLabel_and_displayName(t *testing.T) {
	labels := map[Health]string{
		HealthOperational: "OK", HealthMaintenance: "MAINTENANCE", HealthDegraded: "DEGRADED",
		HealthPartialOutage: "PARTIAL OUTAGE", HealthMajorOutage: "MAJOR OUTAGE", HealthUnknown: "UNKNOWN",
	}
	for health, expected := range labels {
		require.Equal(t, expected, healthLabel(health))
	}
	require.Equal(t, "custom", displayName("custom"))
}
