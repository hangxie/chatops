package chat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/llm"
)

type turnRunner interface {
	Run(ctx context.Context, history []llm.Message, input string) (agent.Turn, error)
}

// repl answers one message per line until EOF, /quit, /exit, or cancellation, keeping successful turns only.
// It owns in for the command's lifetime: a read blocked at return outlives repl until the process exits.
func repl(ctx context.Context, runner turnRunner, historyTurns int, in io.Reader, out io.Writer) error {
	lines := make(chan string)
	ended := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	// Read in a goroutine so cancellation is not stuck behind a blocking read.
	go func() {
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-done:
				return
			}
		}
		ended <- scanner.Err()
	}()

	var turns [][]llm.Message
	for {
		_, _ = fmt.Fprint(out, "> ")
		var line string
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-ended:
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			_, _ = fmt.Fprintln(out)
			return nil
		case line = <-lines:
		}

		input := strings.TrimSpace(line)
		switch input {
		case "":
			continue
		case "/quit", "/exit":
			return nil
		}

		turn, err := runner.Run(ctx, flatten(turns), input)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_, _ = fmt.Fprintf(out, "error: %v\n", err)
			continue
		}
		_, _ = fmt.Fprintln(out, turn.Reply)
		turns = append(turns, turn.Messages)
		if len(turns) > historyTurns {
			turns = turns[len(turns)-historyTurns:]
		}
	}
}

func flatten(turns [][]llm.Message) []llm.Message {
	var history []llm.Message
	for _, turn := range turns {
		history = append(history, turn...)
	}
	return history
}
