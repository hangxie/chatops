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

// serveJSON serves body with status code for every request and returns the server's URL.
func serveJSON(t *testing.T, code int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("User-Agent") != "chatops-mcp-status" {
			http.Error(w, "missing headers", http.StatusBadRequest)
			return
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func fixed(snapshot Snapshot, err error) checkFunc {
	return func(context.Context, *http.Client) (Snapshot, error) { return snapshot, err }
}

func fakeChecker() *checker {
	return &checker{client: http.DefaultClient, services: []service{
		{name: "alpha", display: "Alpha", covers: []string{"Alpha API"}, check: fixed(Snapshot{Health: HealthOperational, Summary: "fine"}, nil)},
		{name: "beta", display: "Beta", check: fixed(Snapshot{}, errors.New("HTTP 503"))},
		{name: "gamma", display: "Gamma", check: fixed(Snapshot{Health: HealthDegraded, Summary: "slow", Incidents: []Incident{{Name: "API latency"}}}, nil)},
	}}
}

func Test_checker_check(t *testing.T) {
	alpha := Snapshot{Service: "Alpha", Health: HealthOperational, Summary: "fine"}
	beta := Snapshot{Service: "Beta", Health: HealthUnknown, Summary: "unable to check: HTTP 503"}
	gamma := Snapshot{Service: "Gamma", Health: HealthDegraded, Summary: "slow", Incidents: []Incident{{Name: "API latency"}}}
	tests := map[string]struct {
		name   string
		want   []Snapshot
		errMsg string
	}{
		"one":         {name: "alpha", want: []Snapshot{alpha}},
		"unreachable": {name: "beta", want: []Snapshot{beta}},
		"all-ordered": {name: "all", want: []Snapshot{alpha, beta, gamma}},
		"unknown":     {name: "delta", errMsg: `unknown service "delta"`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := fakeChecker().check(context.Background(), tc.name)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func Test_checker_check_cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fakeChecker().check(ctx, "all")
	require.ErrorIs(t, err, context.Canceled)
}

func Test_checker_check_bounds_concurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	slow := func(context.Context, *http.Client) (Snapshot, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return Snapshot{Health: HealthOperational}, nil
	}
	c := &checker{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		c.services = append(c.services, service{name: name, display: name, check: slow})
	}

	got, err := c.check(context.Background(), "all")
	require.NoError(t, err)
	require.Len(t, got, 8)
	require.LessOrEqual(t, peak.Load(), int32(maxConcurrentChecks))
}

func Test_newChecker(t *testing.T) {
	c, err := newChecker(Config{Services: []Service{
		{Name: "one", Display: "One", Covers: []string{"Uno"}, Type: TypeStatuspage, URL: "https://one.example/api/v2/summary.json"},
		{Name: "two", Display: "Two", Type: TypeGoogle, Feeds: []Feed{{URL: "https://two.example/incidents.json"}}},
	}})
	require.NoError(t, err)
	require.Equal(t, requestTimeout, c.client.Timeout)
	require.Len(t, c.services, 2)
	require.Equal(t, "one", c.services[0].name)
	require.Equal(t, "One", c.services[0].display)
	require.Equal(t, []string{"Uno"}, c.services[0].covers)
	require.NotNil(t, c.services[0].check)
	require.Equal(t, "two", c.services[1].name)

	_, err = newChecker(Config{})
	require.EqualError(t, err, "at least one service is required")
}

func Test_fetchJSON(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	tests := map[string]struct {
		endpoint string
		errMsg   string
	}{
		"ok":          {endpoint: serveJSON(t, http.StatusOK, `{"a":"b"}`)},
		"http-error":  {endpoint: serveJSON(t, http.StatusServiceUnavailable, "down"), errMsg: "request status: HTTP 503 Service Unavailable"},
		"bad-json":    {endpoint: serveJSON(t, http.StatusOK, "<html>"), errMsg: "decode status response: invalid character '<'"},
		"too-large":   {endpoint: serveJSON(t, http.StatusOK, `{"a":"`+strings.Repeat("x", maxResponseBytes)+`"}`), errMsg: "decode status response: unexpected EOF"},
		"bad-url":     {endpoint: "://nowhere", errMsg: "create request: parse"},
		"unreachable": {endpoint: closed.URL, errMsg: "request status: Get"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got map[string]string
			err := fetchJSON(context.Background(), http.DefaultClient, tc.endpoint, &got)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, map[string]string{"a": "b"}, got)
		})
	}
}

func Test_worstHealth(t *testing.T) {
	require.Equal(t, HealthDegraded, worstHealth(HealthOperational, HealthDegraded))
	require.Equal(t, HealthMajorOutage, worstHealth(HealthMajorOutage, HealthDegraded))
	require.Equal(t, HealthUnknown, worstHealth(HealthMajorOutage, HealthUnknown))
}

func Test_activeSummary(t *testing.T) {
	require.Equal(t, "All Systems Operational", activeSummary("Gemini ", 0))
	require.Equal(t, "Active Gemini incident", activeSummary("Gemini ", 1))
	require.Equal(t, "Active incidents", activeSummary("", 2))
}
