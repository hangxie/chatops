package status

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func Test_GoogleProvider_check(t *testing.T) {
	testCases := map[string]struct {
		cloud     string
		workspace string
		health    Health
		incidents int
		summary   string
	}{
		"no-active-incidents": {cloud: `[{"end":"2026-01-01T00:00:00Z","affected_products":[{"id":"Z0FZJAMvEB4j3NbCJs6B"}]}]`, workspace: `[]`, health: HealthOperational, summary: "All Systems Operational"},
		"vertex-active":       {cloud: `[{"id":"one","external_desc":"Gemini errors","affected_products":[{"id":"Z0FZJAMvEB4j3NbCJs6B"}],"most_recent_update":{"status":"SERVICE_DISRUPTION"}}]`, workspace: `[]`, health: HealthDegraded, incidents: 1, summary: "Active Gemini incident"},
		"workspace-active":    {cloud: `[]`, workspace: `[{"id":"two","external_desc":"Gemini unavailable","affected_products":[{"id":"npdyhgECDJ6tB66MxXyo"}],"most_recent_update":{"status":"SERVICE_OUTAGE"}}]`, health: HealthMajorOutage, incidents: 1, summary: "Active Gemini incident"},
		"multiple-active":     {cloud: `[{"id":"one","external_desc":"Vertex errors","affected_products":[{"id":"Z0FZJAMvEB4j3NbCJs6B"}],"most_recent_update":{"status":"SERVICE_DISRUPTION"}}]`, workspace: `[{"id":"two","external_desc":"Workspace errors","affected_products":[{"id":"npdyhgECDJ6tB66MxXyo"}],"most_recent_update":{"status":"SERVICE_DISRUPTION"}}]`, health: HealthDegraded, incidents: 2, summary: "Active Gemini incidents"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/cloud", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.cloud)) })
			mux.HandleFunc("/workspace", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.workspace)) })
			server := httptest.NewServer(mux)
			defer server.Close()
			provider := NewGoogleProvider(server.URL+"/cloud", server.URL+"/workspace", server.Client())
			snapshot, err := provider.Check(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.health, snapshot.Health)
			require.Len(t, snapshot.Incidents, tc.incidents)
			require.Equal(t, tc.summary, snapshot.Summary)
		})
	}
}

func Test_GoogleProvider_reports_feed_errors(t *testing.T) {
	testCases := map[string]struct {
		cloudStatus, workspaceStatus int
	}{
		"cloud":     {cloudStatus: http.StatusBadGateway, workspaceStatus: http.StatusOK},
		"workspace": {cloudStatus: http.StatusOK, workspaceStatus: http.StatusBadGateway},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/cloud", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.cloudStatus)
				_, _ = w.Write([]byte(`[]`))
			})
			mux.HandleFunc("/workspace", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.workspaceStatus)
				_, _ = w.Write([]byte(`[]`))
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			provider := NewGoogleProvider(server.URL+"/cloud", server.URL+"/workspace", server.Client())
			_, err := provider.Check(context.Background())
			require.Error(t, err)
		})
	}
}

func Test_googleHealth(t *testing.T) {
	testCases := map[string]struct {
		status   string
		expected Health
	}{
		"available":  {status: "AVAILABLE", expected: HealthOperational},
		"disruption": {status: "SERVICE_DISRUPTION", expected: HealthDegraded},
		"outage":     {status: "SERVICE_OUTAGE", expected: HealthMajorOutage},
		"unknown":    {status: "INVESTIGATING", expected: HealthDegraded},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) { require.Equal(t, tc.expected, googleHealth(tc.status)) })
	}
}

func Test_firstLine(t *testing.T) {
	require.Equal(t, "first", firstLine(" first\nsecond"))
	require.Equal(t, strings.Repeat("x", 197)+"...", firstLine(strings.Repeat("x", 201)))
	unicodeLine := strings.Repeat("a", 196) + "🙂" + strings.Repeat("b", 4)
	truncated := firstLine(unicodeLine)
	require.True(t, utf8.ValidString(truncated))
	require.Equal(t, strings.Repeat("a", 196)+"🙂...", truncated)
}
