package kube

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
)

// Now is a seam so age rendering is deterministic in tests.
var Now = time.Now

// formatEach renders one block per target, separated by a blank line.
func formatEach[T any](items []T, format func(T) string) string {
	blocks := make([]string, 0, len(items))
	for _, item := range items {
		blocks = append(blocks, format(item))
	}
	return strings.Join(blocks, "\n\n")
}

// Table renders rows as aligned columns under a header, like kubectl get.
func Table(header []string, rows [][]string) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
	// An empty trailing cell leaves column padding behind; trim it so lines end at their last value.
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// OrNone renders an empty value as n/a.
func OrNone(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

// Age renders a creation time as a kubectl-style age.
func Age(t time.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	d := max(Now().Sub(t), 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
