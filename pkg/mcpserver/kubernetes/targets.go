package kubernetes

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
)

// maxTargets bounds one call's targets so the combined result stays within the agent's tool-result budget.
const maxTargets = 50

// maxConcurrentFetches bounds parallel per-target API calls within one tool call.
const maxConcurrentFetches = 4

// targets validates the requested names and drops duplicates, keeping request order.
func targets(field string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("%s is required: pass at least one %s name", field, strings.TrimSuffix(field, "s"))
	}
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%s[%d] is empty", field, i)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) > maxTargets {
		return nil, fmt.Errorf("%s has %d entries; at most %d are allowed", field, len(out), maxTargets)
	}
	return out, nil
}

// fetchAll runs fetch for every name with bounded concurrency; results keep the order of names.
// The first failure cancels the rest and is the error returned, never a peer's resulting context cancellation.
func fetchAll[T any](ctx context.Context, names []string, fetch func(context.Context, string) (T, error)) ([]T, error) {
	out := make([]T, len(names))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentFetches)
	for i, name := range names {
		g.Go(func() error {
			// A target queued behind the limit must not reach the API once the call has failed or been canceled.
			if err := ctx.Err(); err != nil {
				return err
			}
			v, err := fetch(ctx, name)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			out[i] = v
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
