package engine

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
)

// agenticFixture wires an engine over a fake conn, planner, and single tool
// registered under "fake://", for driving handle directly.
type agenticFixture struct {
	engine  *Engine
	conn    *fakeConn
	planner *fakePlanner
	tool    *fakeTool
}

func newAgenticFixture(t *testing.T, cfg Config, plans []planner.Plan, result tool.Result, toolErr error) *agenticFixture {
	t.Helper()
	conn := &fakeConn{}
	p := &fakePlanner{plans: plans}
	ft := &fakeTool{result: result, err: toolErr}
	tools := tool.NewRegistry(tool.Backend{
		Scheme:     "fake",
		Opener:     func(context.Context, *url.URL, cred.Store) (tool.Tool, error) { return ft, nil },
		Descriptor: stubDesc(),
	})
	cfg.ConnectionID = "conn-1"
	cfg.Chat = conn
	cfg.Planner = p
	cfg.Tools = tools
	e, err := New(cfg)
	require.NoError(t, err)
	return &agenticFixture{engine: e, conn: conn, planner: p, tool: ft}
}

func feedbackStep(id string) planner.Step {
	return planner.Step{Tool: "fake://", Feedback: true, ID: id, Call: tool.Call{Arguments: map[string]string{"unit": "web"}}}
}

func replyStep(text string) planner.Step {
	return planner.Step{Tool: "reply://", Call: tool.Call{Arguments: map[string]string{"text": text}}}
}

func sentTexts(conn *fakeConn) []string {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	texts := make([]string, len(conn.sent))
	for i, m := range conn.sent {
		texts[i] = m.Text
	}
	return texts
}

func Test_handle_feedbackLoop_feedsResultsBackThenReplies(t *testing.T) {
	fx := newAgenticFixture(t, Config{},
		[]planner.Plan{
			{Steps: []planner.Step{feedbackStep("call-1")}},
			{Steps: []planner.Step{replyStep("all healthy")}},
		},
		tool.Result{Text: "raw cluster data"}, nil)

	require.NoError(t, fx.engine.handle(context.Background(), chat.Message{ConversationID: "c1", Text: "how is the cluster"}))

	// The reply is posted; the raw feedback result is not.
	require.Equal(t, []string{"all healthy"}, sentTexts(fx.conn))
	// Round 2 fed the tool result back, correlated to the step id.
	require.Len(t, fx.planner.requests, 2)
	require.Equal(t, "how is the cluster", fx.planner.requests[0].Text)
	require.Nil(t, fx.planner.requests[0].Results)
	require.Empty(t, fx.planner.requests[1].Text)
	require.Equal(t, []planner.StepResult{{ID: "call-1", Content: "raw cluster data"}}, fx.planner.requests[1].Results)
}

func Test_handle_feedbackUserError_isFedBackForRecovery(t *testing.T) {
	fx := newAgenticFixture(t, Config{},
		[]planner.Plan{
			{Steps: []planner.Step{feedbackStep("call-1")}},
			{Steps: []planner.Step{replyStep("try deployments instead")}},
		},
		tool.Result{}, tool.NewUserError(`no resource type "foo"`))

	require.NoError(t, fx.engine.handle(context.Background(), chat.Message{ConversationID: "c1", Text: "list foos"}))

	require.Equal(t, []string{"try deployments instead"}, sentTexts(fx.conn))
	require.Len(t, fx.planner.requests, 2)
	got := fx.planner.requests[1].Results
	require.Len(t, got, 1)
	require.True(t, got[0].IsError)
	require.Equal(t, `no resource type "foo"`, got[0].Content)
}

func Test_handle_fireAndPostUserError_postsSafeMessageAndEndsTurn(t *testing.T) {
	fx := newAgenticFixture(t, Config{},
		[]planner.Plan{{Steps: []planner.Step{{Tool: "fake://", Call: tool.Call{}}}}},
		tool.Result{}, tool.NewUserError("unknown namespace"))

	require.NoError(t, fx.engine.handle(context.Background(), chat.Message{ConversationID: "c1", Text: "x"}))
	require.Equal(t, []string{"unknown namespace"}, sentTexts(fx.conn))
	require.Len(t, fx.planner.requests, 1) // turn ended, no continuation
}

func Test_handle_fireAndPostFatalError_isReturned(t *testing.T) {
	fx := newAgenticFixture(t, Config{},
		[]planner.Plan{{Steps: []planner.Step{{Tool: "fake://", Call: tool.Call{}}}}},
		tool.Result{}, errors.New("connection refused to https://internal"))

	err := fx.engine.handle(context.Background(), chat.Message{ConversationID: "c1", Text: "x"})
	require.ErrorContains(t, err, "connection refused")
	require.Empty(t, sentTexts(fx.conn)) // internal detail not posted
}

func Test_handle_roundCap_forcesFinalPlan(t *testing.T) {
	fx := newAgenticFixture(t, Config{MaxToolRounds: 2},
		[]planner.Plan{
			{Steps: []planner.Step{feedbackStep("a")}},
			{Steps: []planner.Step{feedbackStep("b")}},
			{Steps: []planner.Step{replyStep("summary of what I found")}},
		},
		tool.Result{Text: "data"}, nil)

	require.NoError(t, fx.engine.handle(context.Background(), chat.Message{ConversationID: "c1", Text: "dig deep"}))

	require.Equal(t, []string{"summary of what I found"}, sentTexts(fx.conn))
	require.Len(t, fx.planner.requests, 3)
	require.False(t, fx.planner.requests[0].Final)
	require.False(t, fx.planner.requests[1].Final)
	require.True(t, fx.planner.requests[2].Final, "third plan must be the forced final round")
	require.Equal(t, 2, fx.tool.callCount())
}

func Test_handle_turnByteCap_forcesFinalPlan(t *testing.T) {
	fx := newAgenticFixture(t, Config{MaxTurnBytes: 3},
		[]planner.Plan{
			{Steps: []planner.Step{feedbackStep("a")}},
			{Steps: []planner.Step{replyStep("summary")}},
		},
		tool.Result{Text: "abcdef"}, nil) // 6 bytes > 3

	require.NoError(t, fx.engine.handle(context.Background(), chat.Message{ConversationID: "c1", Text: "x"}))
	require.Len(t, fx.planner.requests, 2)
	require.True(t, fx.planner.requests[1].Final)
}

func Test_handle_resultTruncatedToMaxResultBytes(t *testing.T) {
	big := strings.Repeat("x", 5000)
	fx := newAgenticFixture(t, Config{MaxResultBytes: 100},
		[]planner.Plan{
			{Steps: []planner.Step{feedbackStep("a")}},
			{Steps: []planner.Step{replyStep("done")}},
		},
		tool.Result{Text: big}, nil)

	require.NoError(t, fx.engine.handle(context.Background(), chat.Message{ConversationID: "c1", Text: "x"}))
	fed := fx.planner.requests[1].Results[0].Content
	require.LessOrEqual(t, len(fed), 100)
	require.Contains(t, fed, "truncated")
}

// closingPlanner is a fakePlanner that also records EndTurn calls.
type closingPlanner struct {
	*fakePlanner
	ended [][2]string
}

func (c *closingPlanner) EndTurn(connectionID, conversationID string) {
	c.ended = append(c.ended, [2]string{connectionID, conversationID})
}

func Test_handle_endTurn_calledOnNormalCompletionAndAbort(t *testing.T) {
	t.Run("normal completion", func(t *testing.T) {
		cp := &closingPlanner{fakePlanner: &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{replyStep("hi")}}}}}
		e := newEngineWith(t, cp)
		require.NoError(t, e.handle(context.Background(), chat.Message{ConversationID: "c1"}))
		require.Equal(t, [][2]string{{"conn-1", "c1"}}, cp.ended)
	})

	t.Run("abort", func(t *testing.T) {
		cp := &closingPlanner{fakePlanner: &fakePlanner{err: errors.New("backend down")}}
		e := newEngineWith(t, cp)
		require.Error(t, e.handle(context.Background(), chat.Message{ConversationID: "c9"}))
		require.Equal(t, [][2]string{{"conn-1", "c9"}}, cp.ended)
	})
}

func newEngineWith(t *testing.T, p planner.Planner) *Engine {
	t.Helper()
	e, err := New(Config{ConnectionID: "conn-1", Chat: &fakeConn{}, Planner: p, Tools: tool.NewRegistry()})
	require.NoError(t, err)
	return e
}

func Test_New_validates_agentic_bounds(t *testing.T) {
	base := Config{Chat: &fakeConn{}, Planner: &fakePlanner{}, Tools: tool.NewRegistry()}
	testCases := map[string]struct {
		mutate func(*Config)
		errMsg string
	}{
		"negative rounds": {mutate: func(c *Config) { c.MaxToolRounds = -1 }, errMsg: "negative maximum tool rounds"},
		"negative result": {mutate: func(c *Config) { c.MaxResultBytes = -1 }, errMsg: "negative maximum result bytes"},
		"negative turn":   {mutate: func(c *Config) { c.MaxTurnBytes = -1 }, errMsg: "negative maximum turn bytes"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			_, err := New(cfg)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}

	t.Run("zero applies defaults", func(t *testing.T) {
		e, err := New(base)
		require.NoError(t, err)
		require.Equal(t, DefaultMaxToolRounds, e.maxToolRounds)
		require.Equal(t, DefaultMaxResultBytes, e.maxResultBytes)
		require.Equal(t, DefaultMaxTurnBytes, e.maxTurnBytes)
	})
}
