package kube

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	return Duration(Now().Sub(t))
}

// Duration renders a span at kubectl's age granularity; a negative one clamps to zero.
func Duration(d time.Duration) string {
	d = max(d, 0)
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

// accessModeAbbrev follows kubectl's short access mode names.
var accessModeAbbrev = map[corev1.PersistentVolumeAccessMode]string{
	corev1.ReadWriteOnce:    "RWO",
	corev1.ReadOnlyMany:     "ROX",
	corev1.ReadWriteMany:    "RWX",
	corev1.ReadWriteOncePod: "RWOP",
}

// AccessModes renders volume access modes as kubectl does, e.g. RWO,RWX; unknown modes pass through.
func AccessModes(modes []corev1.PersistentVolumeAccessMode) string {
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		if short, ok := accessModeAbbrev[m]; ok {
			out = append(out, short)
		} else {
			out = append(out, string(m))
		}
	}
	return strings.Join(out, ",")
}
