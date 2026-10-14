package kube

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
)

// MaxTargets bounds one call's targets so the combined result stays within the agent's tool-result budget.
const MaxTargets = 50

// maxConcurrentFetches bounds parallel per-target API calls within one tool call.
const maxConcurrentFetches = 4

// Targets validates cluster-scoped names such as nodes.
func Targets(field string, names []string) ([]string, error) {
	return uniqueTargets(field, names, MaxTargets, func(name string) (string, string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", "is empty"
		}
		return name, ""
	})
}

// uniqueTargets cleans each target, rejects invalid ones, and drops duplicates keeping request order.
func uniqueTargets[K comparable](field string, items []K, limit int, clean func(K) (K, string)) ([]K, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("%s must contain at least one target", field)
	}
	seen := make(map[K]bool, len(items))
	out := make([]K, 0, len(items))
	for i, item := range items {
		item, problem := clean(item)
		if problem != "" {
			return nil, fmt.Errorf("%s[%d] %s", field, i, problem)
		}
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	if len(out) > limit {
		return nil, fmt.Errorf("%s has %d entries; at most %d are allowed", field, len(out), limit)
	}
	return out, nil
}

// FetchAll runs fetch for every key with bounded concurrency; results keep the order of keys.
// The first failure cancels the rest and is the error returned, never a peer's resulting context cancellation.
func FetchAll[K, T any](ctx context.Context, keys []K, fetch func(context.Context, K) (T, error)) ([]T, error) {
	out := make([]T, len(keys))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentFetches)
	for i, key := range keys {
		g.Go(func() error {
			// A target queued behind the limit must not reach the API once the call has failed or been canceled.
			if err := ctx.Err(); err != nil {
				return err
			}
			v, err := fetch(ctx, key)
			if err != nil {
				return fmt.Errorf("%v: %w", key, err)
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
