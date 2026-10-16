package status

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func serveText(t *testing.T, code int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "chatops-mcp-status", r.Header.Get("User-Agent"))
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func Test_healthEndpoint(t *testing.T) {
	tests := map[string]struct {
		code int
		body string
		want Snapshot
	}{
		"llama-cpp-ok": {
			code: http.StatusOK,
			body: `{"status":"ok"}`,
			want: Snapshot{Health: HealthOperational, Summary: "Healthy"},
		},
		"plain-text-ok": {
			code: http.StatusOK,
			body: "ok\n",
			want: Snapshot{Health: HealthOperational, Summary: "Healthy"},
		},
		"empty-body": {
			code: http.StatusNoContent,
			want: Snapshot{Health: HealthOperational, Summary: "Healthy"},
		},
		"status-case-insensitive": {
			code: http.StatusOK,
			body: `{"status":"UP"}`,
			want: Snapshot{Health: HealthOperational, Summary: "Healthy"},
		},
		"harbor-healthy": {
			code: http.StatusOK,
			body: `{"status":"healthy","components":[{"name":"core","status":"healthy"},{"name":"redis","status":"healthy"}]}`,
			want: Snapshot{Health: HealthOperational, Summary: "Healthy"},
		},
		"harbor-unhealthy": {
			code: http.StatusOK,
			body: `{"status":"unhealthy","components":[{"name":"core","status":"healthy"},{"name":"redis","status":"unhealthy","error":"dial tcp: refused"}]}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: `Reports status "unhealthy"`, Incidents: []Incident{{Name: "redis", Status: "unhealthy"}}},
		},
		"unhealthy-component-under-healthy-status": {
			code: http.StatusOK,
			body: `{"status":"healthy","components":[{"name":"jobservice","status":"unhealthy"}]}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: "1 unhealthy component", Incidents: []Incident{{Name: "jobservice", Status: "unhealthy"}}},
		},
		"unhealthy-components-without-status": {
			code: http.StatusOK,
			body: `{"components":[{"name":"redis","status":"down"},{"name":"core","status":"ok"},{"name":"registry","status":""}]}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: "2 unhealthy components", Incidents: []Incident{{Name: "redis", Status: "down"}, {Name: "registry"}}},
		},
		"malformed-component-keeps-status": {
			code: http.StatusOK,
			body: `{"status":"unhealthy","components":[{"name":"redis","status":1},{"name":"core","status":"healthy"}]}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: `Reports status "unhealthy"`, Incidents: []Incident{{Name: "redis", Status: "1"}}},
		},
		"malformed-component-name": {
			code: http.StatusOK,
			body: `{"status":"ok","components":[{"name":7,"status":"down"},"core",null]}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: "1 unhealthy component", Incidents: []Incident{{Name: "7", Status: "down"}}},
		},
		"components-not-a-list": {
			code: http.StatusOK,
			body: `{"status":"unhealthy","components":{"redis":"down"}}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: `Reports status "unhealthy"`},
		},
		"json-array-body": {
			code: http.StatusOK,
			body: `["ok"]`,
			want: Snapshot{Health: HealthOperational, Summary: "Healthy"},
		},
		"numeric-status": {
			code: http.StatusOK,
			body: `{"status":500}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: "Reports status 500"},
		},
		"false-status": {
			code: http.StatusOK,
			body: `{"status":false}`,
			want: Snapshot{Health: HealthPartialOutage, Summary: "Reports status false"},
		},
		"true-status": {
			code: http.StatusOK,
			body: `{"status":true}`,
			want: Snapshot{Health: HealthOperational, Summary: "Healthy"},
		},
		"null-status": {
			code: http.StatusOK,
			body: `{"status":null}`,
			want: Snapshot{Health: HealthOperational, Summary: "Healthy"},
		},
		"multi-line-failure": {
			code: http.StatusServiceUnavailable,
			body: "loading model\n\n  retry later  \n",
			want: Snapshot{Health: HealthMajorOutage, Summary: "HTTP 503 Service Unavailable: loading model; retry later"},
		},
		"server-error-no-body": {
			code: http.StatusInternalServerError,
			want: Snapshot{Health: HealthMajorOutage, Summary: "HTTP 500 Internal Server Error"},
		},
		"long-error-body-truncated": {
			code: http.StatusServiceUnavailable,
			body: strings.Repeat("x", 500),
			want: Snapshot{Health: HealthMajorOutage, Summary: "HTTP 503 Service Unavailable: " + strings.Repeat("x", maxErrorDetail) + "..."},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := healthEndpoint(serveText(t, tc.code, tc.body))(context.Background(), http.DefaultClient)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func Test_healthEndpoint_unreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint := server.URL
	server.Close()

	got, err := healthEndpoint(endpoint)(context.Background(), http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, HealthMajorOutage, got.Health)
	require.True(t, strings.HasPrefix(got.Summary, "Unreachable: "), got.Summary)
}

func Test_healthEndpoint_timeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	got, err := healthEndpoint(server.URL)(context.Background(), &http.Client{Timeout: 50 * time.Millisecond})
	require.NoError(t, err)
	require.Equal(t, HealthMajorOutage, got.Health)
	require.Contains(t, got.Summary, "Unreachable: ")
	require.Contains(t, got.Summary, "Client.Timeout")
}

func Test_healthEndpoint_bad_url(t *testing.T) {
	_, err := healthEndpoint("http://bad host")(context.Background(), http.DefaultClient)
	require.ErrorContains(t, err, "create request")
}
