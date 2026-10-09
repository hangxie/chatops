package chat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/llm"
)

type runCall struct {
	history []llm.Message
	input   string
}

type fakeRunner struct {
	calls []runCall
	run   func(ctx context.Context, input string) (agent.Turn, error)
}

func (f *fakeRunner) Run(ctx context.Context, history []llm.Message, input string) (agent.Turn, error) {
	f.calls = append(f.calls, runCall{history: append([]llm.Message(nil), history...), input: input})
	return f.run(ctx, input)
}

func echoTurn(_ context.Context, input string) (agent.Turn, error) {
	reply := "re: " + input
	return agent.Turn{Reply: reply, Messages: []llm.Message{
		{Role: llm.RoleUser, Content: input},
		{Role: llm.RoleAssistant, Content: reply},
	}}, nil
}

func turnMessages(inputs ...string) []llm.Message {
	var messages []llm.Message
	for _, input := range inputs {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: input}, llm.Message{Role: llm.RoleAssistant, Content: "re: " + input})
	}
	return messages
}

func Test_repl(t *testing.T) {
	tests := map[string]struct {
		input        string
		historyTurns int
		run          func(context.Context, string) (agent.Turn, error)
		output       string
		calls        []runCall
	}{
		"eof-immediately": {
			input:  "",
			output: "> \n",
		},
		"one-turn": {
			input:  "hello\n",
			run:    echoTurn,
			output: "> re: hello\n> \n",
			calls:  []runCall{{input: "hello"}},
		},
		"blank-lines-skipped": {
			input:  "\n   \nhi\n",
			run:    echoTurn,
			output: "> > > re: hi\n> \n",
			calls:  []runCall{{input: "hi"}},
		},
		"quit": {
			input:  "/quit\nnever\n",
			output: "> ",
		},
		"exit": {
			input:  "  /exit  \n",
			output: "> ",
		},
		"history-carried": {
			input:        "a\nb\nc\n",
			historyTurns: 5,
			run:          echoTurn,
			output:       "> re: a\n> re: b\n> re: c\n> \n",
			calls: []runCall{
				{input: "a"},
				{input: "b", history: turnMessages("a")},
				{input: "c", history: turnMessages("a", "b")},
			},
		},
		"history-bounded": {
			input:        "a\nb\nc\n",
			historyTurns: 1,
			run:          echoTurn,
			output:       "> re: a\n> re: b\n> re: c\n> \n",
			calls: []runCall{
				{input: "a"},
				{input: "b", history: turnMessages("a")},
				{input: "c", history: turnMessages("b")},
			},
		},
		"turn-error-continues": {
			input: "bad\ngood\n",
			run: func(ctx context.Context, input string) (agent.Turn, error) {
				if input == "bad" {
					return agent.Turn{}, errors.New("model: 503")
				}
				return echoTurn(ctx, input)
			},
			output: "> error: model: 503\n> re: good\n> \n",
			calls:  []runCall{{input: "bad"}, {input: "good"}},
		},
		"failed-turn-not-in-history": {
			input:        "bad\ngood\n",
			historyTurns: 5,
			run: func(ctx context.Context, input string) (agent.Turn, error) {
				if input == "bad" {
					return agent.Turn{}, errors.New("boom")
				}
				return echoTurn(ctx, input)
			},
			output: "> error: boom\n> re: good\n> \n",
			calls:  []runCall{{input: "bad"}, {input: "good"}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{run: tc.run}
			var out bytes.Buffer
			historyTurns := tc.historyTurns
			if historyTurns == 0 {
				historyTurns = 10
			}
			err := repl(context.Background(), runner, historyTurns, strings.NewReader(tc.input), &out)
			require.NoError(t, err)
			require.Equal(t, tc.output, out.String())
			require.Equal(t, tc.calls, runner.calls)
		})
	}
}

func Test_repl_cancelled_while_waiting_for_input(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- repl(ctx, &fakeRunner{run: echoTurn}, 10, reader, io.Discard) }()

	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("repl did not stop")
	}
}

func Test_repl_cancelled_during_turn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeRunner{run: func(ctx context.Context, _ string) (agent.Turn, error) {
		cancel()
		<-ctx.Done()
		return agent.Turn{}, ctx.Err()
	}}
	var out bytes.Buffer
	err := repl(ctx, runner, 10, strings.NewReader("hi\nagain\n"), &out)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "> ", out.String())
	require.Len(t, runner.calls, 1)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("tty gone") }

func Test_repl_read_error(t *testing.T) {
	err := repl(context.Background(), &fakeRunner{run: echoTurn}, 10, failingReader{}, io.Discard)
	require.EqualError(t, err, "read input: tty gone")
}
