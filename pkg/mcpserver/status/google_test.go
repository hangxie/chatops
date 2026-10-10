package status

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_google(t *testing.T) {
	const (
		cloud     = `[{"end":"","external_desc":"Vertex Gemini errors\nmore detail","affected_products":[{"id":"vertex","title":"Vertex Gemini API"}],"most_recent_update":{"status":"SERVICE_OUTAGE"}},{"end":"2026-01-01","external_desc":"old","affected_products":[{"id":"vertex","title":"Vertex Gemini API"}]},{"end":"","external_desc":"BigQuery slow","affected_products":[{"id":"bq","title":"BigQuery"}]}]`
		workspace = `[{"end":"","external_desc":"Mail delayed","affected_products":[{"id":"gmail","title":"Gmail"},{"id":"cal","title":"Google Calendar"}],"most_recent_update":{"status":"SERVICE_DISRUPTION"}},{"end":"2026-01-01","external_desc":"old","affected_products":[{"id":"drive","title":"Google Drive"}]}]`
	)
	cloudURL := serveJSON(t, http.StatusOK, cloud)
	workspaceURL := serveJSON(t, http.StatusOK, workspace)
	emptyURL := serveJSON(t, http.StatusOK, `[]`)
	tests := map[string]struct {
		feeds  []Feed
		want   Snapshot
		errMsg string
	}{
		"operational": {
			feeds: []Feed{{URL: emptyURL}},
			want:  Snapshot{Health: HealthOperational, Summary: "All Systems Operational", Incidents: []Incident{}},
		},
		"every-product": {
			feeds: []Feed{{URL: workspaceURL}},
			want:  Snapshot{Health: HealthDegraded, Summary: "Active Test incident", Incidents: []Incident{{Name: "Gmail, Google Calendar: Mail delayed", Status: "service_disruption"}}},
		},
		"filtered-feeds-combined": {
			feeds: []Feed{{URL: cloudURL, Products: []string{"vertex"}}, {URL: workspaceURL, Products: []string{"gmail"}}},
			want: Snapshot{Health: HealthMajorOutage, Summary: "Active Test incidents", Incidents: []Incident{
				{Name: "Vertex Gemini API: Vertex Gemini errors", Status: "service_outage"},
				{Name: "Gmail, Google Calendar: Mail delayed", Status: "service_disruption"},
			}},
		},
		"filtered-out": {
			feeds: []Feed{{URL: workspaceURL, Products: []string{"drive"}}},
			want:  Snapshot{Health: HealthOperational, Summary: "All Systems Operational", Incidents: []Incident{}},
		},
		"http-error": {
			feeds:  []Feed{{URL: emptyURL}, {URL: serveJSON(t, http.StatusServiceUnavailable, "")}},
			errMsg: "HTTP 503",
		},
		"bad-json": {
			feeds:  []Feed{{URL: serveJSON(t, http.StatusOK, `{}`)}},
			errMsg: "decode status response",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := google(tc.feeds, "Test")(context.Background(), http.DefaultClient)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func Test_googleHealth(t *testing.T) {
	tests := map[string]Health{
		"SERVICE_OUTAGE":     HealthMajorOutage,
		"service_disruption": HealthDegraded,
		"AVAILABLE":          HealthOperational,
		"SOMETHING_NEW":      HealthDegraded,
	}
	for status, want := range tests {
		t.Run(status, func(t *testing.T) {
			require.Equal(t, want, googleHealth(status))
		})
	}
}

func Test_firstLine(t *testing.T) {
	require.Equal(t, "first", firstLine("  first\nsecond"))
	long := firstLine(strings.Repeat("é", 300))
	require.Equal(t, 200, len([]rune(long)))
	require.True(t, strings.HasSuffix(long, "..."))
}
