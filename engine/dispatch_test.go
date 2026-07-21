package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
)

// syncBuffer is an io.Writer safe for the concurrent workers writing log
// records during a run.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records parses the buffer's newline-delimited JSON log lines.
func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var recs []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		recs = append(recs, rec)
	}
	return recs
}

// hasRecord reports whether some record has the given message and all the
// given attributes (numbers compare as JSON float64).
func hasRecord(recs []map[string]any, msg string, attrs map[string]any) bool {
	for _, rec := range recs {
		if rec["msg"] != msg {
			continue
		}
		match := true
		for k, v := range attrs {
			if rec[k] != v {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func Test_stepTools(t *testing.T) {
	require.Empty(t, stepTools(nil))
	require.Equal(t, []string{"reply://", "ping://"}, stepTools([]planner.Step{
		{Tool: "reply://"},
		{Tool: "ping://"},
	}))
}

// runWithLogger runs an engine that handles one message producing a reply
// step and a tool step, returning the captured log records.
func runWithLogger(t *testing.T, level slog.Level) []map[string]any {
	t.Helper()
	var out syncBuffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: level}))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1", Sender: "alice", Text: "do it"}}}
	p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{
		{Tool: "reply://", Call: tool.Call{Action: "send", Parameters: map[string]string{"text": "working"}}},
		{Tool: "fake://", Call: tool.Call{Action: "restart", Target: "web"}},
	}}}}
	taskTool := &fakeTool{result: tool.Result{Text: "restarted web"}}
	tools := tool.NewRegistry(tool.Backend{Scheme: "fake", Opener: func(_ context.Context, _ *url.URL, _ cred.Store) (tool.Tool, error) {
		return taskTool, nil
	}})
	e, err := New(Config{ConnectionID: "chat-1", Chat: conn, Planner: p, Tools: tools, Logger: logger})
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.sent) == 2
	}, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)
	return out.records(t)
}

func Test_handle_logs_planner_and_tool_flow(t *testing.T) {
	recs := runWithLogger(t, slog.LevelDebug)

	require.True(t, hasRecord(recs, "engine started", map[string]any{"connection_id": "chat-1"}))
	require.True(t, hasRecord(recs, "message received", map[string]any{"conversation_id": "c1", "sender": "alice"}))
	require.True(t, hasRecord(recs, "plan produced", map[string]any{"conversation_id": "c1", "steps": float64(2)}))
	require.True(t, hasRecord(recs, "executing step", map[string]any{"tool": "fake://", "action": "restart", "target": "web"}))
	// The reply step posts directly and returns no text, so the posted-result
	// record belongs to the tool step that produced output.
	require.True(t, hasRecord(recs, "result posted", map[string]any{"tool": "fake://"}))
	require.True(t, hasRecord(recs, "step produced no output", map[string]any{"tool": "reply://"}))
	require.True(t, hasRecord(recs, "engine stopped", nil))
	// Detail records are emitted only at debug level.
	require.True(t, hasRecord(recs, "planning message", map[string]any{"text": "do it"}))
	require.True(t, hasRecord(recs, "opening tool", map[string]any{"tool": "fake://"}))
}

func Test_handle_info_level_omits_debug_records(t *testing.T) {
	recs := runWithLogger(t, slog.LevelInfo)

	require.True(t, hasRecord(recs, "message received", map[string]any{"conversation_id": "c1"}))
	require.False(t, hasRecord(recs, "planning message", nil))
	require.False(t, hasRecord(recs, "opening tool", nil))
}

func Test_handle_logs_planner_failure(t *testing.T) {
	var out syncBuffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1", Text: "do it"}}}
	p := &fakePlanner{err: errors.New("backend down")}
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry(), Logger: logger})
	require.NoError(t, err)

	require.Error(t, e.Run(ctx))
	require.True(t, hasRecord(out.records(t), "planner failed", map[string]any{"error": "backend down"}))
}
