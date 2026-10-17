package agent

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/hangxie/chatops/internal/llm"
)

// maxFailureLineRunes caps one tool-failure line appended to a reply.
const maxFailureLineRunes = 200

// callKey identifies a call by tool and canonical arguments, ignoring the ID that differs on every retry.
type callKey struct{ name, args string }

func keyOf(call llm.ToolCall) callKey {
	return callKey{name: call.Name, args: canonicalJSON(call.Arguments)}
}

// canonicalJSON re-encodes raw with sorted keys and no spacing; invalid JSON is returned unchanged.
func canonicalJSON(raw string) string {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return raw
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// failureLog tracks a turn's failing calls: how often each failed in a row, and which failures are unresolved.
type failureLog struct {
	counts map[callKey]int
	lines  map[callKey]string
	order  []callKey
}

func newFailureLog() *failureLog {
	return &failureLog{counts: map[callKey]int{}, lines: map[callKey]string{}}
}

// fail records a failed call; an empty line counts toward repeats but is never reported.
func (f *failureLog) fail(call llm.ToolCall, line string) {
	key := keyOf(call)
	f.counts[key]++
	if line == "" {
		return
	}
	if _, ok := f.lines[key]; !ok {
		f.order = append(f.order, key)
	}
	f.lines[key] = line
}

// succeed resolves earlier failures of the same tool with the same arguments.
func (f *failureLog) succeed(call llm.ToolCall) {
	key := keyOf(call)
	delete(f.counts, key)
	if _, ok := f.lines[key]; ok {
		delete(f.lines, key)
		f.order = slices.DeleteFunc(f.order, func(k callKey) bool { return k == key })
	}
}

// repeats reports whether any call already failed maxRepeatedFailures times in a row.
func (f *failureLog) repeats(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if f.counts[keyOf(call)] >= maxRepeatedFailures {
			return true
		}
	}
	return false
}

// unresolved returns the latest line of each unresolved failure, in order of first failure.
func (f *failureLog) unresolved() []string {
	var lines []string
	for _, key := range f.order {
		lines = append(lines, f.lines[key])
	}
	return lines
}

// failureLine formats a reportable failure as "`server.tool`: first line"; it is empty when nothing is reportable.
func failureLine(step Step, failure string) string {
	if failure == "" || step.Server == "" {
		return ""
	}
	text, _, _ := strings.Cut(failure, "\n")
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > maxFailureLineRunes {
		text = string(runes[:maxFailureLineRunes-1]) + "…"
	}
	return "`" + step.Server + "." + step.Tool + "`: " + text
}

// withFailures appends unresolved tool failures to reply, so they reach the user whatever the model wrote.
func withFailures(reply string, lines []string) string {
	if len(lines) == 0 {
		return reply
	}
	var b strings.Builder
	if reply != "" {
		b.WriteString(reply)
		b.WriteString("\n\n")
	}
	b.WriteString("Tool errors:")
	for _, line := range lines {
		b.WriteString("\n- ")
		b.WriteString(line)
	}
	return b.String()
}
