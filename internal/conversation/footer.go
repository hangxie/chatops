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
	runs := toolRuns(stats.Steps)
	steps := make([]string, len(runs))
	for i, run := range runs {
		if len(run) == 1 {
			steps[i] = stepText(run[0])
		} else {
			steps[i] = runText(run)
		}
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

// toolRuns splits steps into runs, where consecutive calls of the same tool share one run.
func toolRuns(steps []agent.Step) [][]agent.Step {
	var runs [][]agent.Step
	for i, step := range steps {
		last := len(runs) - 1
		if i > 0 && step.Phase == agent.PhaseTool && steps[i-1].Phase == agent.PhaseTool && toolName(step) == toolName(steps[i-1]) {
			runs[last] = append(runs[last], step)
			continue
		}
		runs = append(runs, []agent.Step{step})
	}
	return runs
}

// runText sums a run of calls to one tool, keeping the slowest call and every failure visible.
func runText(run []agent.Step) string {
	var total time.Duration
	fastest, slowest := run[0].Duration, run[0].Duration
	var result, shown int
	failures := map[agent.Outcome]int{}
	var failed []agent.Outcome
	for _, step := range run {
		total += step.Duration
		fastest, slowest = min(fastest, step.Duration), max(slowest, step.Duration)
		result += step.ResultBytes
		shown += min(step.ResultBytes, step.SentBytes)
		if step.Outcome != agent.OutcomeOK {
			if failures[step.Outcome] == 0 {
				failed = append(failed, step.Outcome)
			}
			failures[step.Outcome]++
		}
	}
	details := []string{formatDuration(fastest)}
	if slowest != fastest {
		details[0] += "–" + formatDuration(slowest)
	}
	if result > 0 {
		details = append(details, toolBytes(result, shown))
	}
	for _, outcome := range failed {
		details = append(details, fmt.Sprintf("%d %s", failures[outcome], outcome))
	}
	return fmt.Sprintf("%s ×%d %s (%s)", toolName(run[0]), len(run), formatDuration(total), strings.Join(details, ", "))
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
