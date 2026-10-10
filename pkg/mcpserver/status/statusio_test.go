package status

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_statusIO(t *testing.T) {
	tests := map[string]struct {
		code   int
		body   string
		want   Snapshot
		errMsg string
	}{
		"operational": {
			code: http.StatusOK,
			body: `{"result":{"status_overall":{"status":"Operational","status_code":100},"incidents":[]}}`,
			want: Snapshot{Health: HealthOperational, Summary: "Operational", Incidents: []Incident{}},
		},
		"incident": {
			code: http.StatusOK,
			body: `{"result":{"status_overall":{"status":"Partial Service Disruption","status_code":400},"incidents":[{"name":"Pulls failing"}]}}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: "Partial Service Disruption", Incidents: []Incident{{Name: "Pulls failing"}}},
		},
		"unknown-code": {
			code: http.StatusOK,
			body: `{"result":{"status_overall":{"status":"?","status_code":999}}}`,
			want: Snapshot{Health: HealthUnknown, Summary: "?", Incidents: []Incident{}},
		},
		"http-error": {code: http.StatusNotFound, body: "", errMsg: "HTTP 404"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := statusIO(serveJSON(t, tc.code, tc.body))(context.Background(), http.DefaultClient)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
