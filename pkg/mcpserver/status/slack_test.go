package status

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_slackStatus(t *testing.T) {
	tests := map[string]struct {
		code   int
		body   string
		want   Snapshot
		errMsg string
	}{
		"ok": {
			code: http.StatusOK,
			body: `{"status":"ok","active_incidents":[]}`,
			want: Snapshot{Health: HealthOperational, Summary: "All Systems Operational", Incidents: []Incident{}},
		},
		"incident-and-outage": {
			code: http.StatusOK,
			body: `{"status":"active","active_incidents":[{"title":"iOS crash","type":"incident","status":"active","url":"https://slack-status.com/a"},{"title":"Messages failing","type":"outage","status":"active"}]}`,
			want: Snapshot{Health: HealthMajorOutage, Summary: "Active incidents", Incidents: []Incident{
				{Name: "iOS crash", Status: "active", URL: "https://slack-status.com/a"},
				{Name: "Messages failing", Status: "active"},
			}},
		},
		"not-ok-without-incidents": {
			code: http.StatusOK,
			body: `{"status":"active","active_incidents":[]}`,
			want: Snapshot{Health: HealthUnknown, Summary: "All Systems Operational", Incidents: []Incident{}},
		},
		"http-error": {code: http.StatusInternalServerError, body: "", errMsg: "HTTP 500"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := slackStatus(serveJSON(t, tc.code, tc.body))(context.Background(), http.DefaultClient)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
