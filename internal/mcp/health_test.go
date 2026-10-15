package mcp

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func Test_Manager_Health(t *testing.T) {
	manager := connectTest(t, map[string]*mcpsdk.Server{
		"one": newTestServer("one"),
		"two": newTestServer("two"),
	})
	require.Equal(t, []ServerHealth{
		{ID: "one", Up: true, Tools: 2},
		{ID: "two", Up: true, Tools: 2},
	}, manager.Health())

	require.NoError(t, manager.Close())
	require.Equal(t, []ServerHealth{
		{ID: "one", Tools: 2},
		{ID: "two", Tools: 2},
	}, manager.Health(), "a closed manager reports every server down")
}

func Test_Manager_Health_disconnect(t *testing.T) {
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	session, err := newTestServer("one").Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	var logs bytes.Buffer
	manager, err := connect(context.Background(), map[string]mcpsdk.Transport{"one": clientTransport}, slog.New(slog.NewJSONHandler(&logs, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })

	require.NoError(t, session.Close())
	require.Eventually(t, func() bool {
		return !manager.Health()[0].Up
	}, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, logs.String(), `"msg":"mcp server disconnected","server":"one"`)
}
