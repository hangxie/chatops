package conversation

import (
	"fmt"
	"strings"
	"time"

	"github.com/hangxie/chatops/internal/agent"
)

// outcomeNotes flags a turn that did not end in an answer.
var outcomeNotes = map[agent.Outcome]string{
	agent.OutcomeStopped: "stopped",
	agent.OutcomeTimeout: "timed out",
	agent.OutcomeError:   "failed",
}

// footer renders a turn's steps on one line and its totals on the next; replyBytes is the reply before the footer.
func footer(stats agent.Stats, replyBytes int) string {
	steps := make([]string, len(stats.Steps))
	for i, step := range stats.Steps {
		steps[i] = stepText(step)
	}
	totals := []string{"total " + formatDuration(stats.Duration), plural(stats.ToolCalls(), "tool")}
	if prompt, completion, ok := stats.Tokens(); ok {
		totals = append(totals, formatCount(prompt)+"→"+formatCount(completion)+" tok")
	}
	totals = append(totals, "reply "+formatBytes(replyBytes))
	if note := outcomeNotes[stats.Outcome]; note != "" {
		totals = append(totals, note)
	}
	summary := strings.Join(totals, " · ")
	if len(steps) == 0 {
		return summary
	}
	return strings.Join(steps, " → ") + "\n" + summary
}

func stepText(step agent.Step) string {
	name := string(step.Phase)
	var details []string
	switch {
	case step.Phase == agent.PhaseTool:
		name = toolName(step)
		if step.ResultBytes > 0 {
			details = append(details, toolBytes(step.ResultBytes, step.SentBytes))
		}
	case step.Usage != nil:
		details = append(details, formatCount(step.Usage.PromptTokens)+"→"+formatCount(step.Usage.CompletionTokens)+" tok")
	}
	if step.Outcome != agent.OutcomeOK {
		details = append(details, string(step.Outcome))
	}
	// Only a failover names the model, since otherwise the turn ran on the one the schedule picked.
	if len(step.Attempts) > 1 {
		details = append([]string{"via " + step.Model}, details...)
	}
	text := name + " " + formatDuration(step.Duration)
	if len(details) > 0 {
		text += " (" + strings.Join(details, ", ") + ")"
	}
	return text
}

// toolName is server.tool, unique across servers; "__" in model-facing names would render as bold in chat.
func toolName(step agent.Step) string {
	if step.Server == "" {
		return step.Name
	}
	return step.Server + "." + step.Tool
}

// toolBytes shows what the model saw only when truncation made it smaller.
func toolBytes(result, sent int) string {
	if sent < result {
		return formatBytes(result) + "→" + formatBytes(sent)
	}
	return formatBytes(result)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func formatDuration(d time.Duration) string {
	if d > 0 && d < time.Millisecond {
		return "<1ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func formatBytes(n int) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return scaled(float64(n)/(1<<10), "KB")
	default:
		return scaled(float64(n)/(1<<20), "MB")
	}
}

func formatCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// scaled keeps one decimal below 10 and drops it above, so sizes stay short.
func scaled(v float64, unit string) string {
	if v < 10 {
		return fmt.Sprintf("%.1f%s", v, unit)
	}
	return fmt.Sprintf("%.0f%s", v, unit)
}
