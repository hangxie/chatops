package status

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_StatuspageProvider_check(t *testing.T) {
	testCases := map[string]struct {
		body    string
		health  Health
		summary string
	}{
		"operational":       {body: `{"status":{"indicator":"none","description":"All Systems Operational"},"incidents":[]}`, health: HealthOperational, summary: "All Systems Operational"},
		"incident":          {body: `{"status":{"indicator":"major","description":"Partial System Outage"},"incidents":[{"name":"API errors","status":"investigating","shortlink":"https://stspg.example/i"}]}`, health: HealthPartialOutage, summary: "Partial System Outage"},
		"unknown-indicator": {body: `{"status":{"indicator":"new","description":"Unexpected"}}`, health: HealthUnknown, summary: "Unexpected"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			provider := NewStatuspageProvider("github", nil, server.URL, server.Client())
			snapshot, err := provider.Check(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.health, snapshot.Health)
			require.Equal(t, tc.summary, snapshot.Summary)
		})
	}
}

func Test_StatuspageProvider_reports_fetch_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	_, err := NewStatuspageProvider("github", nil, server.URL, server.Client()).Check(context.Background())
	require.Error(t, err)
}
