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

// FairShares splits limit evenly across lists of the given sizes, handing what a short list leaves to the longer ones.
func FairShares(sizes []int, limit int) []int {
	shares := make([]int, len(sizes))
	for open := len(sizes); limit > 0 && open > 0; {
		each := max(limit/open, 1)
		open = 0
		for i, n := range sizes {
			take := min(each, n-shares[i], limit)
			shares[i] += take
			limit -= take
			if shares[i] < n {
				open++
			}
		}
	}
	return shares
}
