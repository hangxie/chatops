package engine

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
)

// canonicalContent renders a tool.Result to the one canonical UTF-8 string the
// engine both measures and feeds back: Result.Text, followed by Result.Details
// as deterministic sorted "key=value" lines. Rendering it here (not in the
// planner) makes the bytes measured the bytes sent, the single source of truth
// for size.
func canonicalContent(r tool.Result) string {
	parts := make([]string, 0, len(r.Details)+1)
	if r.Text != "" {
		parts = append(parts, r.Text)
	}
	keys := make([]string, 0, len(r.Details))
	for key := range r.Details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, key+"="+r.Details[key])
	}
	return strings.Join(parts, "\n")
}

// boundContent truncates s to at most limit bytes, reserving room for a marker
// so the returned string — marker included — never exceeds limit. It cuts on a
// UTF-8 rune boundary so a multibyte rune is never split. When limit is too
// small even for the marker, it hard-cuts on a rune boundary with no marker.
func boundContent(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	// The marker depends only on the (fixed) total length, not on the kept
	// length, so there is no circular sizing.
	marker := fmt.Sprintf("…[truncated, %d bytes total]", len(s))
	keep := limit - len(marker)
	if keep <= 0 {
		return s[:runeBoundary(s, limit)]
	}
	return s[:runeBoundary(s, keep)] + marker
}

// runeBoundary returns n backed up to the start of a UTF-8 rune, so s[:result]
// never splits a multibyte rune. n at or past len(s) returns len(s).
func runeBoundary(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// endTurn tells a stateful planner the turn for conversationID has ended so it
// can drop the in-flight transcript. Fixed planners that keep no per-turn state
// do not implement planner.TurnCloser and are skipped.
func (e *Engine) endTurn(conversationID string) {
	if closer, ok := e.planner.(planner.TurnCloser); ok {
		closer.EndTurn(e.connectionID, conversationID)
	}
}
