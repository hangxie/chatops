package kube

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// HumanCPU renders a CPU quantity as millicores, matching kubectl (e.g. 276m); empty or unparseable pass through.
func HumanCPU(s string) string {
	if s == "" {
		return ""
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return s
	}
	return fmt.Sprintf("%dm", q.MilliValue())
}

// WithPercent appends a percentage in parentheses when both parts are known.
func WithPercent(value, percent string) string {
	if value == "" || percent == "" {
		return value
	}
	return value + " (" + percent + ")"
}

// Percent computes used/total as a whole-percent string; MilliValue keeps fractional CPU correct.
func Percent(used, total string) string {
	u, uerr := resource.ParseQuantity(used)
	t, terr := resource.ParseQuantity(total)
	if uerr != nil || terr != nil || t.MilliValue() == 0 {
		return ""
	}
	return fmt.Sprintf("%.0f%%", float64(u.MilliValue())/float64(t.MilliValue())*100)
}
