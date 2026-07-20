package status

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeProvider struct {
	name    string
	aliases []string
	snap    Snapshot
	err     error
}

func (p fakeProvider) Name() string                            { return p.name }
func (p fakeProvider) Aliases() []string                       { return p.aliases }
func (p fakeProvider) Check(context.Context) (Snapshot, error) { return p.snap, p.err }

func Test_Checker_check(t *testing.T) {
	failed := errors.New("unavailable")
	checker, err := NewChecker([]Provider{
		fakeProvider{name: "github", aliases: []string{"gh"}, snap: Snapshot{Health: HealthOperational, Summary: "All Systems Operational"}},
		fakeProvider{name: "openai", err: failed},
	})
	require.NoError(t, err)

	testCases := map[string]struct {
		target string
		names  []string
		health []Health
		errIs  error
	}{
		"canonical": {target: "github", names: []string{"github"}, health: []Health{HealthOperational}},
		"alias":     {target: "GH", names: []string{"github"}, health: []Health{HealthOperational}},
		"all":       {target: "all", names: []string{"github", "openai"}, health: []Health{HealthOperational, HealthUnknown}},
		"unknown":   {target: "missing", errIs: ErrUnknownProvider},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			snapshots, err := checker.Check(context.Background(), tc.target)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				return
			}
			require.NoError(t, err)
			require.Len(t, snapshots, len(tc.names))
			for i := range snapshots {
				require.Equal(t, tc.names[i], snapshots[i].Provider)
				require.Equal(t, tc.health[i], snapshots[i].Health)
			}
		})
	}
}

func Test_NewChecker_validates_catalog(t *testing.T) {
	testCases := map[string][]Provider{
		"nil-provider":    {nil},
		"empty-name":      {fakeProvider{name: ""}},
		"duplicate-name":  {fakeProvider{name: "one"}, fakeProvider{name: "one"}},
		"duplicate-alias": {fakeProvider{name: "one", aliases: []string{"shared"}}, fakeProvider{name: "two", aliases: []string{"shared"}}},
		"empty-alias":     {fakeProvider{name: "one", aliases: []string{""}}},
		"reserved-alias":  {fakeProvider{name: "one", aliases: []string{"all"}}},
		"alias-is-name":   {fakeProvider{name: "one"}, fakeProvider{name: "two", aliases: []string{"one"}}},
		"name-is-alias":   {fakeProvider{name: "one", aliases: []string{"two"}}, fakeProvider{name: "two"}},
		"reserved-all":    {fakeProvider{name: "all"}},
	}
	for name, providers := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := NewChecker(providers)
			require.Error(t, err)
		})
	}
}

func Test_Checker_names_returns_copy(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "one"}})
	require.NoError(t, err)
	names := checker.Names()
	names[0] = "changed"
	require.Equal(t, []string{"one"}, checker.Names())
}

func Test_Checker_honors_cancellation(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "one"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = checker.Check(ctx, "one")
	require.ErrorIs(t, err, context.Canceled)
}

func Test_fetchJSON_reports_response_errors(t *testing.T) {
	testCases := map[string]struct {
		status int
		body   string
		errMsg string
	}{
		"http-status":    {status: http.StatusBadGateway, body: `{}`, errMsg: "HTTP 502"},
		"malformed-json": {status: http.StatusOK, body: `{`, errMsg: "decode status response"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			var destination map[string]any
			err := fetchJSON(context.Background(), server.Client(), server.URL, &destination)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type closeErrorBody struct{ io.Reader }

func (b closeErrorBody) Close() error { return errors.New("close failed") }

func Test_fetchJSON_reports_request_and_close_errors(t *testing.T) {
	testCases := map[string]struct {
		endpoint string
		client   *http.Client
		errMsg   string
	}{
		"invalid-request": {endpoint: "://", client: http.DefaultClient, errMsg: "create request"},
		"transport": {
			endpoint: "https://example.test",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("transport failed")
			})},
			errMsg: "request status",
		},
		"close": {
			endpoint: "https://example.test",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: closeErrorBody{Reader: strings.NewReader(`{}`)}}, nil
			})},
			errMsg: "close status response",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var destination map[string]any
			err := fetchJSON(context.Background(), tc.client, tc.endpoint, &destination)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_defaultChecker_catalog(t *testing.T) {
	require.Equal(t, []string{"github", "anthropic", "cloudflare", "openai", "gemini", "slack", "docker-hub"}, defaultChecker().Names())
}

type concurrencyProvider struct {
	name   string
	active *atomic.Int32
	peak   *atomic.Int32
	gate   <-chan struct{}
}

func (p concurrencyProvider) Name() string      { return p.name }
func (p concurrencyProvider) Aliases() []string { return nil }
func (p concurrencyProvider) Check(context.Context) (Snapshot, error) {
	active := p.active.Add(1)
	for peak := p.peak.Load(); active > peak && !p.peak.CompareAndSwap(peak, active); peak = p.peak.Load() {
	}
	<-p.gate
	p.active.Add(-1)
	return Snapshot{Health: HealthOperational}, nil
}

func Test_Checker_bounds_all_concurrency(t *testing.T) {
	gate := make(chan struct{})
	var active, peak atomic.Int32
	providers := make([]Provider, 0, 7)
	for _, name := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		providers = append(providers, concurrencyProvider{name: name, active: &active, peak: &peak, gate: gate})
	}
	checker, err := NewChecker(providers)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, checkErr := checker.Check(context.Background(), "all")
		done <- checkErr
	}()
	require.Eventually(t, func() bool { return peak.Load() == maxConcurrentChecks }, time.Second, time.Millisecond)
	close(gate)
	require.NoError(t, <-done)
	require.Equal(t, int32(maxConcurrentChecks), peak.Load())
}

func Test_worstHealth(t *testing.T) {
	testCases := map[string]struct {
		left, right Health
		expected    Health
	}{
		"right-worse": {left: HealthOperational, right: HealthDegraded, expected: HealthDegraded},
		"left-worse":  {left: HealthMajorOutage, right: HealthOperational, expected: HealthMajorOutage},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.expected, worstHealth(tc.left, tc.right))
		})
	}
}

type cancelProvider struct {
	cancel context.CancelFunc
}

func (p cancelProvider) Name() string      { return "cancel" }
func (p cancelProvider) Aliases() []string { return nil }
func (p cancelProvider) Check(context.Context) (Snapshot, error) {
	p.cancel()
	return Snapshot{}, errors.New("cancelled upstream")
}

func Test_Checker_propagates_cancellation_from_provider(t *testing.T) {
	for _, target := range []string{"cancel", "all"} {
		t.Run(target, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			checker, err := NewChecker([]Provider{cancelProvider{cancel: cancel}})
			require.NoError(t, err)
			_, err = checker.Check(ctx, target)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

func Test_mustChecker(t *testing.T) {
	checker := &Checker{}
	require.Same(t, checker, mustChecker(checker, nil))
	require.Panics(t, func() { mustChecker(nil, errors.New("invalid catalog")) })
}
