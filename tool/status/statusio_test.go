package status

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_StatusIOProvider_check(t *testing.T) {
	testCases := map[string]struct {
		code      int
		health    Health
		incidents string
	}{
		"operational": {code: 100, health: HealthOperational},
		"maintenance": {code: 200, health: HealthMaintenance},
		"degraded":    {code: 300, health: HealthDegraded},
		"disruption":  {code: 400, health: HealthPartialOutage},
		"outage":      {code: 500, health: HealthMajorOutage},
		"unknown":     {code: 999, health: HealthUnknown, incidents: `[{"name":"Status unavailable"}]`},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				incidents := tc.incidents
				if incidents == "" {
					incidents = `[]`
				}
				_, _ = w.Write([]byte(`{"result":{"status_overall":{"status":"Current status","status_code":` + requireJSONInt(tc.code) + `},"incidents":` + incidents + `}}`))
			}))
			defer server.Close()
			provider := NewStatusIOProvider("docker-hub", []string{"docker"}, server.URL, server.Client())
			snapshot, err := provider.Check(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.health, snapshot.Health)
			if tc.incidents != "" {
				require.Len(t, snapshot.Incidents, 1)
			}
		})
	}
}

func Test_StatusIOProvider_reports_fetch_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	_, err := NewStatusIOProvider("docker-hub", nil, server.URL, server.Client()).Check(context.Background())
	require.Error(t, err)
}

func requireJSONInt(value int) string {
	return fmt.Sprintf("%d", value)
}
