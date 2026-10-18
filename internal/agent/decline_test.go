package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/llm"
	"github.com/hangxie/chatops/internal/mcp"
)

func Test_splitDecline(t *testing.T) {
	ping := llm.ToolCall{ID: "1", Name: "builtin__ping"}
	decline := llm.ToolCall{ID: "2", Name: declineTool}
	tests := map[string]struct {
		calls    []llm.ToolCall
		rest     []llm.ToolCall
		declined bool
	}{
		"no decline":    {calls: []llm.ToolCall{ping}, rest: []llm.ToolCall{ping}},
		"only decline":  {calls: []llm.ToolCall{decline}, declined: true},
		"two declines":  {calls: []llm.ToolCall{decline, decline}, declined: true},
		"mixed":         {calls: []llm.ToolCall{decline, ping}, rest: []llm.ToolCall{ping}, declined: true},
		"no tool calls": {},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rest, declined := splitDecline(tc.calls)
			require.Equal(t, tc.rest, rest)
			require.Equal(t, tc.declined, declined)
		})
	}
}

func Test_declineSpec(t *testing.T) {
	require.NotContains(t, declineSpec.Name, "__")
	require.Contains(t, declineSpec.Description, "Never use it when another tool can answer")
	require.JSONEq(t, `{"type":"object","properties":{},"additionalProperties":false}`, string(declineSpec.Parameters))
}

func Test_Agent_Run_decline(t *testing.T) {
	ping := llm.ToolCall{ID: "c1", Name: "builtin__ping", Arguments: `{}`}
	decline := llm.ToolCall{ID: "c2", Name: declineTool, Arguments: `{}`}
	failing := func(context.Context, mcp.Tool, json.RawMessage) (mcp.Result, error) {
		return mcp.Result{Text: "Unauthorized", IsError: true}, nil
	}
	flaky := func() func(context.Context, mcp.Tool, json.RawMessage) (mcp.Result, error) {
		n := 0
		return func(context.Context, mcp.Tool, json.RawMessage) (mcp.Result, error) {
			if n++; n == 1 {
				return failing(context.Background(), mcp.Tool{}, nil)
			}
			return mcp.Result{Text: "pong"}, nil
		}
	}
	failureReply := "Tool errors:\n- `builtin.ping`: Unauthorized"
	declined := Step{Phase: PhaseTool, Name: declineTool, Server: builtinServer, Tool: declineTool, Outcome: OutcomeOK}
	plan := Step{Phase: PhasePlan, Outcome: OutcomeOK}

	tests := map[string]struct {
		responses []func(context.Context) (llm.Response, error)
		handle    func(context.Context, mcp.Tool, json.RawMessage) (mcp.Result, error)
		health    []mcp.ServerHealth
		reply     string
		pinged    int
		lastSteps []Step
	}{
		"sole decline ends the turn": {
			responses: []func(context.Context) (llm.Response, error){calls(decline)},
			reply:     testDecline,
			lastSteps: []Step{plan, declined},
		},
		"two declines are one decline": {
			responses: []func(context.Context) (llm.Response, error){calls(decline, decline)},
			reply:     testDecline,
			lastSteps: []Step{plan, declined},
		},
		"decline names down servers": {
			responses: []func(context.Context) (llm.Response, error){calls(decline)},
			health:    []mcp.ServerHealth{{ID: "builtin", Up: true}, {ID: "kubernetes"}, {ID: "status"}},
			reply:     testDecline + "\n\nUnavailable tool servers: kubernetes, status",
		},
		"decline mixed with a tool call is dropped": {
			responses: []func(context.Context) (llm.Response, error){calls(decline, ping), reply("pong")},
			reply:     "pong",
			pinged:    1,
		},
		"decline after an unresolved failure shows the failure": {
			responses: []func(context.Context) (llm.Response, error){calls(ping), calls(decline)},
			handle:    failing,
			reply:     failureReply,
			pinged:    1,
		},
		"text decline after an unresolved failure shows the failure": {
			responses: []func(context.Context) (llm.Response, error){calls(ping), reply(" " + testDecline + "\n")},
			handle:    failing,
			reply:     failureReply,
			pinged:    1,
		},
		"quoted text decline names down servers": {
			responses: []func(context.Context) (llm.Response, error){calls(ping), reply(`"` + testDecline + `"`)},
			health:    []mcp.ServerHealth{{ID: "kubernetes"}},
			reply:     testDecline + "\n\nUnavailable tool servers: kubernetes",
			pinged:    1,
		},
		"decline after a resolved failure is a normal decline": {
			responses: []func(context.Context) (llm.Response, error){calls(ping), calls(ping), calls(decline)},
			handle:    flaky(),
			reply:     testDecline,
			pinged:    2,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			handle := tc.handle
			if handle == nil {
				handle = pong
			}
			tools := newFakeTools(t, handle)
			tools.health = tc.health
			limits := testLimits()
			limits.RequireToolCall = true
			model := &fakeModel{responses: tc.responses}

			turn, err := New(model, tools, limits, nil).Run(context.Background(), nil, "hello")
			require.NoError(t, err)
			require.Equal(t, tc.reply, turn.Reply)
			require.Len(t, tools.calls, tc.pinged)
			require.Equal(t, []llm.Message{{Role: llm.RoleUser, Content: "hello"}}, turn.Messages)
			require.Equal(t, OutcomeAnswered, turn.Stats.Outcome)
			if tc.lastSteps != nil {
				require.Equal(t, tc.lastSteps, withoutDurations(turn.Stats.Steps))
			}
			// Only the first model call is forced.
			require.Equal(t, llm.ToolChoiceRequired, model.requests[0].ToolChoice)
			for _, req := range model.requests[1:] {
				require.Empty(t, req.ToolChoice)
			}
			// Every call left in the transcript has a result, so a dropped decline never reaches the model.
			for _, req := range model.requests {
				for _, msg := range req.Messages {
					for _, call := range msg.ToolCalls {
						require.NotEqual(t, declineTool, call.Name)
					}
				}
			}
		})
	}
}

func Test_Agent_Run_forced_first_call(t *testing.T) {
	tests := map[string]struct {
		require bool
		reply   string
		errMsg  string
	}{
		"text despite required fails the turn": {require: true, errMsg: "model: model returned no tool call although one was required"},
		"text allowed when not required":       {require: false, reply: "pong from memory"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			limits := testLimits()
			limits.RequireToolCall = tc.require
			model := &fakeModel{responses: []func(context.Context) (llm.Response, error){reply("pong from memory")}}

			turn, err := New(model, newFakeTools(t, pong), limits, nil).Run(context.Background(), nil, "ping")
			if tc.errMsg != "" {
				require.ErrorIs(t, err, errNoToolCall)
				require.EqualError(t, err, tc.errMsg)
				require.Empty(t, turn.Reply)
				require.Equal(t, OutcomeError, turn.Stats.Outcome)
				require.Equal(t, []Step{{Phase: PhaseAnswer, Outcome: OutcomeError}}, withoutDurations(turn.Stats.Steps))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.reply, turn.Reply)
			require.Empty(t, model.requests[0].ToolChoice)
		})
	}
}

func Test_Agent_Run_dropped_decline_is_logged(t *testing.T) {
	var logs bytes.Buffer
	model := &fakeModel{responses: []func(context.Context) (llm.Response, error){
		calls(llm.ToolCall{ID: "c1", Name: declineTool}, llm.ToolCall{ID: "c2", Name: "builtin__ping", Arguments: `{}`}),
		reply("pong"),
	}}
	agent := New(model, newFakeTools(t, pong), testLimits(), slog.New(slog.NewTextHandler(&logs, nil)))
	turn, err := agent.Run(context.Background(), nil, "ping")
	require.NoError(t, err)
	require.Equal(t, "pong", turn.Reply)
	require.Contains(t, logs.String(), "decline dropped because the model also called other tools")
	// A dropped decline is not a step, so it never counts as a decline in stats or metrics.
	for _, step := range turn.Stats.Steps {
		require.NotEqual(t, declineTool, step.Name)
	}
}
