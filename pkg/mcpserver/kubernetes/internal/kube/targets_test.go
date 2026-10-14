package kube

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_targets(t *testing.T) {
	tests := map[string]struct {
		names  []string
		want   []string
		errMsg string
	}{
		"single":              {names: []string{"worker-0"}, want: []string{"worker-0"}},
		"keeps order":         {names: []string{"worker-2", "worker-0", "worker-1"}, want: []string{"worker-2", "worker-0", "worker-1"}},
		"drops duplicates":    {names: []string{"worker-1", "worker-0", "worker-1"}, want: []string{"worker-1", "worker-0"}},
		"trims whitespace":    {names: []string{" worker-0 ", "worker-0"}, want: []string{"worker-0"}},
		"nil":                 {names: nil, errMsg: "nodes must contain at least one target"},
		"empty":               {names: []string{}, errMsg: "nodes must contain at least one target"},
		"blank entry":         {names: []string{"worker-0", " "}, errMsg: "nodes[1] is empty"},
		"too many":            {names: manyNames(MaxTargets + 1), errMsg: "nodes has 51 entries; at most 50 are allowed"},
		"at the limit":        {names: manyNames(MaxTargets), want: manyNames(MaxTargets)},
		"duplicates collapse": {names: append(manyNames(MaxTargets), "n0"), want: manyNames(MaxTargets)},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := Targets("nodes", tc.names)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func manyNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("n%d", i)
	}
	return names
}

func Test_fetchAll_keeps_order(t *testing.T) {
	var inFlight, peak atomic.Int32
	got, err := FetchAll(context.Background(), []string{"a", "b", "c", "d", "e", "f"}, func(_ context.Context, name string) (string, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		return name + "!", nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"a!", "b!", "c!", "d!", "e!", "f!"}, got)
	require.LessOrEqual(t, peak.Load(), int32(maxConcurrentFetches))
}

func Test_fetchAll_error_names_target(t *testing.T) {
	_, err := FetchAll(context.Background(), []string{"a", "b"}, func(_ context.Context, name string) (string, error) {
		if name == "b" {
			return "", errors.New("connection refused")
		}
		return name, nil
	})
	require.EqualError(t, err, "b: connection refused")
}

func Test_fetchAll_failure_cancels_in_flight(t *testing.T) {
	// "bad" fails only once three peers are in flight; "late-*" queue behind the concurrency limit.
	names := []string{"bad", "a", "b", "c", "late-0", "late-1"}
	inFlight := make(chan struct{}, 3)
	var mu sync.Mutex
	fetched := map[string]error{}

	_, err := FetchAll(context.Background(), names, func(ctx context.Context, name string) (string, error) {
		if name == "bad" {
			for range 3 {
				<-inFlight
			}
			return "", errors.New("connection refused")
		}
		inFlight <- struct{}{}
		<-ctx.Done()
		mu.Lock()
		defer mu.Unlock()
		fetched[name] = ctx.Err()
		return "", ctx.Err()
	})

	require.EqualError(t, err, "bad: connection refused")
	require.False(t, errors.Is(err, context.Canceled))
	// In-flight peers were canceled; queued targets never reached the API.
	require.Equal(t, map[string]error{"a": context.Canceled, "b": context.Canceled, "c": context.Canceled}, fetched)
}

func Test_fetchAll_parent_canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32

	_, err := FetchAll(ctx, []string{"a", "b"}, func(context.Context, string) (string, error) {
		calls.Add(1)
		return "", nil
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, calls.Load())
}
