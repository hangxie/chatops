package status

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_datadog(t *testing.T) {
	tests := map[string]struct {
		code   int
		body   string
		want   Snapshot
		errMsg string
	}{
		"operational": {
			code: http.StatusOK,
			body: `{"components":[{"name":"Web","status":"operational"}],"incidents":[{"title":"Old","currentStatus":"resolved","resolved":true}]}`,
			want: Snapshot{Health: HealthOperational, Summary: "All Systems Operational", Incidents: []Incident{}},
		},
		"grouped-component-degraded": {
			code: http.StatusOK,
			body: `{"components":[{"name":"API","type":"ComponentGroup","components":[{"name":"Chat","status":"partial_outage"}]},{"name":"Web","status":"degraded"}],"incidents":[{"title":"Chat errors","currentStatus":"identified","resolved":false}]}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: "Partial outage: Chat; degraded: Web", Incidents: []Incident{{Name: "Chat errors", Status: "identified"}}},
		},
		"incident-on-operational-components": {
			code: http.StatusOK,
			body: `{"components":[{"name":"API","status":"operational"}],"incidents":[{"title":"Elevated API latency","currentStatus":"investigating","resolved":false}]}`,
			want: Snapshot{Health: HealthOperational, Summary: "Active incident", Incidents: []Incident{{Name: "Elevated API latency", Status: "investigating"}}},
		},
		"group-own-status": {
			code: http.StatusOK,
			body: `{"components":[{"name":"API","status":"degraded","components":[{"name":"Chat","status":"operational"}]}]}`,
			want: Snapshot{Health: HealthDegraded, Summary: "Degraded: API", Incidents: []Incident{}},
		},
		"major-outage": {
			code: http.StatusOK,
			body: `{"components":[{"name":"Web","status":"major_outage"}],"incidents":null}`,
			want: Snapshot{Health: HealthMajorOutage, Summary: "Major outage: Web", Incidents: []Incident{}},
		},
		"maintenance": {
			code: http.StatusOK,
			body: `{"components":[{"name":"Web","status":"maintenance"},{"name":"API","status":"operational"}]}`,
			want: Snapshot{Health: HealthMaintenance, Summary: "Maintenance: Web", Incidents: []Incident{}},
		},
		"unknown-status": {
			code: http.StatusOK,
			body: `{"components":[{"name":"Web","status":"weird"}]}`,
			want: Snapshot{Health: HealthUnknown, Summary: "Unknown: Web", Incidents: []Incident{}},
		},
		"no-components": {
			code: http.StatusOK,
			body: `{}`,
			want: Snapshot{Health: HealthUnknown, Summary: "No components reported", Incidents: []Incident{}},
		},
		"incident-without-components": {
			code: http.StatusOK,
			body: `{"incidents":[{"title":"Login errors","currentStatus":"investigating","resolved":false}]}`,
			want: Snapshot{Health: HealthUnknown, Summary: "Active incident", Incidents: []Incident{{Name: "Login errors", Status: "investigating"}}},
		},
		"http-error": {code: http.StatusForbidden, body: "", errMsg: "HTTP 403"},
		"bad-json":   {code: http.StatusOK, body: "<html>", errMsg: "decode status response"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := datadog(serveJSON(t, tc.code, tc.body))(context.Background(), http.DefaultClient)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
