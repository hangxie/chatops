package status

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// packagedConfig is the services file the packages install as /etc/chatops/status.yaml.
const packagedConfig = "../../../package/systemd/status.yaml"

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "status.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func Test_Load_packaged(t *testing.T) {
	cfg, err := Load(packagedConfig)
	require.NoError(t, err)
	var names []string
	for _, s := range cfg.Services {
		names = append(names, s.Name)
		require.NotEmpty(t, s.Covers, s.Name)
	}
	require.Equal(t, []string{"github", "anthropic", "cloudflare", "openai", "gemini", "google-workspace", "slack", "docker-hub"}, names)
	require.Equal(t, []Feed{
		{URL: "https://status.cloud.google.com/incidents.json", Products: []string{"Z0FZJAMvEB4j3NbCJs6B"}},
		{URL: "https://www.google.com/appsstatus/dashboard/incidents.json", Products: []string{"npdyhgECDJ6tB66MxXyo"}},
	}, cfg.Services[4].Feeds)
}

func Test_Load_errors(t *testing.T) {
	tests := map[string]struct {
		path   string
		errMsg string
	}{
		"missing":       {path: filepath.Join(t.TempDir(), "none.yaml"), errMsg: "read status config: open"},
		"bad-yaml":      {path: writeFile(t, "services: [\n"), errMsg: "parse status config"},
		"unknown-field": {path: writeFile(t, "services:\n  - name: x\n    display: X\n    type: slack\n    url: https://x.example\n    timeout: 5s\n"), errMsg: "field timeout not found"},
		"empty-file":    {path: writeFile(t, ""), errMsg: "at least one service is required"},
		"invalid":       {path: writeFile(t, "services:\n  - name: X\n"), errMsg: `services[0]: name "X" must be lowercase`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(tc.path)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Config_validate(t *testing.T) {
	valid := Service{Name: "svc-1", Display: "Svc", Type: TypeStatuspage, URL: "https://svc.example/api/v2/summary.json"}
	google := Service{Name: "g", Display: "G", Type: TypeGoogle, Feeds: []Feed{{URL: "https://g.example/incidents.json"}}}
	with := func(base Service, change func(*Service)) Service {
		change(&base)
		return base
	}
	tests := map[string]struct {
		services []Service
		errMsg   string
	}{
		"valid":          {services: []Service{valid, google, with(valid, func(s *Service) { s.Name, s.Type, s.URL = "io", TypeStatusIO, "http://io.local/status" })}},
		"empty":          {errMsg: "at least one service is required"},
		"duplicate":      {services: []Service{valid, valid}, errMsg: `services[1]: duplicate name "svc-1"`},
		"bad-name":       {services: []Service{with(valid, func(s *Service) { s.Name = "Svc 1" })}, errMsg: `services[0]: name "Svc 1" must be lowercase letters, digits, and "-", and not "all"`},
		"reserved-name":  {services: []Service{with(valid, func(s *Service) { s.Name = "all" })}, errMsg: `name "all" must be`},
		"no-display":     {services: []Service{with(valid, func(s *Service) { s.Display = "" })}, errMsg: `service "svc-1": display is required`},
		"bad-type":       {services: []Service{with(valid, func(s *Service) { s.Type = "rss" })}, errMsg: `service "svc-1": type "rss" must be statuspage, statusio, slack, or google`},
		"no-url":         {services: []Service{with(valid, func(s *Service) { s.URL = "" })}, errMsg: `service "svc-1": url "" must be an absolute http or https URL`},
		"relative-url":   {services: []Service{with(valid, func(s *Service) { s.URL = "/api" })}, errMsg: `url "/api" must be an absolute`},
		"ftp-url":        {services: []Service{with(valid, func(s *Service) { s.URL = "ftp://svc.example/x" })}, errMsg: `url "ftp://svc.example/x" must be an absolute`},
		"unparsable-url": {services: []Service{with(valid, func(s *Service) { s.URL = "https://%zz" })}, errMsg: `url "https://%zz" must be an absolute`},
		"feeds-on-page":  {services: []Service{with(valid, func(s *Service) { s.Feeds = google.Feeds })}, errMsg: `service "svc-1": feeds is only for type "google"`},
		"google-url":     {services: []Service{with(google, func(s *Service) { s.URL = "https://g.example" })}, errMsg: `service "g": type "google" uses feeds, not url`},
		"google-nofeeds": {services: []Service{with(google, func(s *Service) { s.Feeds = nil })}, errMsg: `service "g": type "google" needs at least one feed`},
		"google-bad-url": {services: []Service{with(google, func(s *Service) { s.Feeds = []Feed{{URL: "g.example"}} })}, errMsg: `service "g": url "g.example" must be an absolute`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := Config{Services: tc.services}.validate()
			if tc.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Service_check(t *testing.T) {
	tests := map[string]struct {
		service Service
		want    Health
	}{
		"statuspage": {service: Service{Type: TypeStatuspage, URL: serveJSON(t, http.StatusOK, `{"status":{"indicator":"minor"}}`)}, want: HealthDegraded},
		"statusio":   {service: Service{Type: TypeStatusIO, URL: serveJSON(t, http.StatusOK, `{"result":{"status_overall":{"status_code":500}}}`)}, want: HealthMajorOutage},
		"slack":      {service: Service{Type: TypeSlack, URL: serveJSON(t, http.StatusOK, `{"status":"ok"}`)}, want: HealthOperational},
		"google":     {service: Service{Type: TypeGoogle, Display: "G", Feeds: []Feed{{URL: serveJSON(t, http.StatusOK, `[{"end":"","affected_products":[{"id":"p"}],"most_recent_update":{"status":"SERVICE_OUTAGE"}}]`)}}}, want: HealthMajorOutage},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tc.service.check()(context.Background(), http.DefaultClient)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Health)
		})
	}
}
