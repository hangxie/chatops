package kube

import "fmt"

// DefaultMaxResults caps the rows a list or pods tool returns when Clients.MaxResults is unset.
const DefaultMaxResults = 50

// ResultLimit is the row cap for list and pods tools, DefaultMaxResults unless MaxResults is positive.
func (c Clients) ResultLimit() int {
	if c.MaxResults > 0 {
		return c.MaxResults
	}
	return DefaultMaxResults
}

// Cap keeps the first limit items and returns them with the count before the cap.
func Cap[T any](items []T, limit int) ([]T, int) {
	return items[:min(len(items), limit)], len(items)
}

// CapNote tells the model rows were left out, so it narrows the call instead of assuming it saw everything.
func CapNote(shown, total int, plural string) string {
	if total <= shown {
		return ""
	}
	return fmt.Sprintf("\n(showing the first %d of %d %s)", shown, total, plural)
}
