package openaichatcompletions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

const (
	respToolCall = `{"choices":[{"message":{"role":"assistant","tool_calls":` +
		`[{"id":"tc1","type":"function","function":{"name":"status-check","arguments":"{\"service\":\"github\"}"}}]}}]}`
	respReply = `{"choices":[{"message":{"role":"assistant","content":"all good"}}]}`
)

// scriptedServer serves canned completion responses in order and records each
// request body, so a multi-round exchange can be driven and inspected.
type scriptedServer struct {
	mu        sync.Mutex
	responses []string
	bodies    []chatRequest
}

func (s *scriptedServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body chatRequest
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.bodies = append(s.bodies, body)
	i := len(s.bodies) - 1
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	_, _ = io.WriteString(w, s.responses[i])
}

func newScriptedPlanner(t *testing.T, responses ...string) (*Planner, *scriptedServer) {
	t.Helper()
	sc := &scriptedServer{responses: responses}
	server := httptest.NewServer(http.HandlerFunc(sc.handler))
	t.Cleanup(server.Close)
	statusDesc := &tool.Descriptor{Description: "check", Parameters: []tool.Param{{Name: "service", Type: "string", Required: true}}}
	tools := tool.NewRegistry(tool.Backend{Scheme: "status-check", Opener: fakeToolOpener, Descriptor: statusDesc})
	creds := testutils.CredentialStore{Values: map[cred.Key]string{cred.PlannerAPIKey: "sk"}}
	p, ok := plannerAgainst(t, server, creds, tools).(*Planner)
	require.True(t, ok)
	t.Cleanup(func() { _ = p.Close() })
	return p, sc
}

func Test_Plan_continuation_feedsToolResultBack(t *testing.T) {
	p, sc := newScriptedPlanner(t, respToolCall, respReply)

	plan1, err := p.Plan(context.Background(), planner.Request{Text: "is github up?", ConnectionID: "conn", ConversationID: "C1"})
	require.NoError(t, err)
	require.Len(t, plan1.Steps, 1)
	require.True(t, plan1.Steps[0].Feedback)
	require.Equal(t, "tc1", plan1.Steps[0].ID)

	plan2, err := p.Plan(context.Background(), planner.Request{
		ConnectionID: "conn", ConversationID: "C1",
		Results: []planner.StepResult{{ID: "tc1", Content: "github operational"}},
	})
	require.NoError(t, err)
	require.Equal(t, []planner.Step{{Tool: reply.URL, Call: tool.Call{Arguments: map[string]string{"text": "all good"}}}}, plan2.Steps)

	// The continuation request replayed the transcript and appended the result.
	msgs := sc.bodies[1].Messages
	require.GreaterOrEqual(t, len(msgs), 4) // system, user, assistant(tool_calls), tool(result)
	last := msgs[len(msgs)-1]
	require.Equal(t, "tool", last.Role)
	require.Equal(t, "tc1", last.ToolCallID)
	require.Equal(t, "github operational", last.Content)
	require.NotEmpty(t, sc.bodies[1].Tools, "tools are still offered on a continuation")
}

func Test_Plan_finalRound_offersNoToolsAndSummarizes(t *testing.T) {
	p, sc := newScriptedPlanner(t, respToolCall,
		`{"choices":[{"message":{"role":"assistant","content":"here is the summary"}}]}`)

	_, err := p.Plan(context.Background(), planner.Request{Text: "dig", ConnectionID: "conn", ConversationID: "C1"})
	require.NoError(t, err)

	plan, err := p.Plan(context.Background(), planner.Request{
		ConnectionID: "conn", ConversationID: "C1", Final: true,
		Results: []planner.StepResult{{ID: "tc1", Content: "data"}},
	})
	require.NoError(t, err)
	require.Equal(t, []planner.Step{{Tool: reply.URL, Call: tool.Call{Arguments: map[string]string{"text": "here is the summary"}}}}, plan.Steps)
	require.Empty(t, sc.bodies[1].Tools, "the final round offers no tools")
	last := sc.bodies[1].Messages[len(sc.bodies[1].Messages)-1]
	require.Contains(t, last.Content, "no more tools")
}

func Test_Plan_endTurn_dropsTranscript(t *testing.T) {
	p, _ := newScriptedPlanner(t, respToolCall)

	_, err := p.Plan(context.Background(), planner.Request{Text: "x", ConnectionID: "conn", ConversationID: "C1"})
	require.NoError(t, err)
	p.EndTurn("conn", "C1")

	_, err = p.Plan(context.Background(), planner.Request{
		ConnectionID: "conn", ConversationID: "C1",
		Results: []planner.StepResult{{ID: "tc1", Content: "data"}},
	})
	require.ErrorContains(t, err, "no in-flight transcript")
}

func Test_toolResultMessages(t *testing.T) {
	t.Run("reply synthesized, operational matched by id", func(t *testing.T) {
		calls := []toolCall{
			{ID: "r1", Function: functionCall{Name: "reply"}},
			{ID: "t1", Function: functionCall{Name: "status-check"}},
		}
		msgs, err := toolResultMessages(calls, []planner.StepResult{{ID: "t1", Content: "ok"}})
		require.NoError(t, err)
		require.Equal(t, []reqMessage{
			{Role: "tool", ToolCallID: "r1", Content: "delivered"},
			{Role: "tool", ToolCallID: "t1", Content: "ok"},
		}, msgs)
	})

	t.Run("error result is marked", func(t *testing.T) {
		msgs, err := toolResultMessages([]toolCall{{ID: "t1", Function: functionCall{Name: "status-check"}}},
			[]planner.StepResult{{ID: "t1", Content: "no resource type", IsError: true}})
		require.NoError(t, err)
		require.Equal(t, "error: no resource type", msgs[0].Content)
	})

	t.Run("empty content becomes a placeholder", func(t *testing.T) {
		msgs, err := toolResultMessages([]toolCall{{ID: "t1", Function: functionCall{Name: "status-check"}}},
			[]planner.StepResult{{ID: "t1", Content: ""}})
		require.NoError(t, err)
		require.Equal(t, "(no output)", msgs[0].Content)
	})

	t.Run("missing result errors", func(t *testing.T) {
		_, err := toolResultMessages([]toolCall{{ID: "t9", Function: functionCall{Name: "status-check"}}}, nil)
		require.ErrorContains(t, err, "missing result")
	})
}

func Test_buildMessages_humanTurnStartsFresh(t *testing.T) {
	p := &Planner{transcripts: newTranscriptStore(time.Minute, 8)}
	msgs, err := p.buildMessages(planner.Request{Text: "hello"})
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, "system", msgs[0].Role)
	require.Equal(t, "hello", msgs[1].Content)
}

func Test_lastToolCalls_noAssistantMessage(t *testing.T) {
	require.Nil(t, lastToolCalls([]reqMessage{{Role: "user", Content: "hi"}}))
}

func Test_uniqueID(t *testing.T) {
	seen := map[string]bool{}
	require.Equal(t, "call_0", uniqueID("", 0, seen)) // generated
	require.Equal(t, "x", uniqueID("x", 1, seen))
	require.Equal(t, "x_1", uniqueID("x", 2, seen)) // duplicate disambiguated
}
