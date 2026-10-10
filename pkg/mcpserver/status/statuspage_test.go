package status

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_statuspage(t *testing.T) {
	tests := map[string]struct {
		code   int
		body   string
		want   Snapshot
		errMsg string
	}{
		"operational": {
			code: http.StatusOK,
			body: `{"status":{"indicator":"none","description":"All Systems Operational"},"incidents":[]}`,
			want: Snapshot{Health: HealthOperational, Summary: "All Systems Operational", Incidents: []Incident{}},
		},
		"incident": {
			code: http.StatusOK,
			body: `{"status":{"indicator":"major","description":"Partial System Outage"},"incidents":[{"name":"Actions delayed","status":"investigating","shortlink":"https://stspg.io/x"}]}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: "Partial System Outage", Incidents: []Incident{{Name: "Actions delayed", Status: "investigating", URL: "https://stspg.io/x"}}},
		},
		"maintenance": {
			code: http.StatusOK,
			body: `{"status":{"indicator":"maintenance","description":"Service Under Maintenance"}}`,
			want: Snapshot{Health: HealthMaintenance, Summary: "Service Under Maintenance", Incidents: []Incident{}},
		},
		"unknown-indicator": {
			code: http.StatusOK,
			body: `{"status":{"indicator":"weird","description":"?"}}`,
			want: Snapshot{Health: HealthUnknown, Summary: "?", Incidents: []Incident{}},
		},
		"http-error": {code: http.StatusBadGateway, body: "", errMsg: "HTTP 502"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := statuspage(serveJSON(t, tc.code, tc.body))(context.Background(), http.DefaultClient)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
