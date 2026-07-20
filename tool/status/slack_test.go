package status

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_SlackProvider_check(t *testing.T) {
	testCases := map[string]struct {
		body    string
		health  Health
		summary string
	}{
		"ok":       {body: `{"status":"ok","active_incidents":[]}`, health: HealthOperational, summary: "All Systems Operational"},
		"incident": {body: `{"status":"active","active_incidents":[{"title":"Message delays","type":"incident","status":"active","url":"https://slack-status.com/i"}]}`, health: HealthDegraded, summary: "Active incident"},
		"outage":   {body: `{"status":"active","active_incidents":[{"title":"Slack unavailable","type":"outage","status":"active"}]}`, health: HealthMajorOutage, summary: "Active incident"},
		"multiple": {body: `{"status":"active","active_incidents":[{"title":"One","type":"incident"},{"title":"Two","type":"incident"}]}`, health: HealthDegraded, summary: "Active incidents"},
		"unknown":  {body: `{"status":"active","active_incidents":[]}`, health: HealthUnknown, summary: "All Systems Operational"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			provider := NewSlackProvider(server.URL, server.Client())
			snapshot, err := provider.Check(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.health, snapshot.Health)
			require.Equal(t, tc.summary, snapshot.Summary)
		})
	}
}

func Test_SlackProvider_reports_fetch_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	_, err := NewSlackProvider(server.URL, server.Client()).Check(context.Background())
	require.Error(t, err)
}
