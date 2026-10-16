package status

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// packagedConfig is the directory the packages install as /etc/chatops/mcp-status.d.
const packagedConfig = "../../../package/systemd/mcp-status.d"

// writeDir writes files into a new config directory and returns it.
func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	return dir
}

func Test_Load_packaged(t *testing.T) {
	cfg, err := Load(packagedConfig)
	require.NoError(t, err)
	var names []string
	for _, s := range cfg.Services {
		names = append(names, s.Name)
		require.NotEmpty(t, s.Covers, s.Name)
	}
	require.Equal(t, []string{"github", "anthropic", "cloudflare", "openai", "openrouter", "gemini", "google-workspace", "slack", "docker-hub"}, names)
	require.Equal(t, []Feed{
		{URL: "https://status.cloud.google.com/incidents.json", Products: []string{"Z0FZJAMvEB4j3NbCJs6B"}},
		{URL: "https://www.google.com/appsstatus/dashboard/incidents.json", Products: []string{"npdyhgECDJ6tB66MxXyo"}},
	}, cfg.Services[5].Feeds)
}

func svc(name string) string {
	return "services:\n  - name: " + name + "\n    display: " + name + "\n    type: slack\n    url: https://" + name + ".example\n"
}

func Test_Load(t *testing.T) {
	tests := map[string]struct {
		files  map[string]string
		want   []string
		errMsg string
	}{
		"file-name-order":  {files: map[string]string{"20-b.yaml": svc("b"), "00-default.yaml": svc("default"), "10-a.yaml": svc("a")}, want: []string{"default", "a", "b"}},
		"empty-default":    {files: map[string]string{"00-default.yaml": "", "homelab.yaml": svc("a")}, want: []string{"a"}},
		"empty-dir":        {errMsg: "at least one service is required"},
		"nothing-in-files": {files: map[string]string{"empty.yaml": ""}, errMsg: "at least one service is required"},
		"duplicate-across": {files: map[string]string{"00-default.yaml": svc("a"), "homelab.yaml": svc("a")}, errMsg: `services[1]: duplicate name "a"`},
		"invalid":          {files: map[string]string{"00-default.yaml": svc("a"), "homelab.yaml": "services:\n  - name: B\n"}, errMsg: `services[1]: name "B" must be lowercase`},
		"bad-yaml":         {files: map[string]string{"a.yaml": "services: [\n"}, errMsg: "parse status config"},
		"unknown-field":    {files: map[string]string{"homelab.yaml": "include: [x.yaml]\n"}, errMsg: "homelab.yaml: yaml: unmarshal errors:\n  line 1: field include not found"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := writeDir(t, tc.files)
			cfg, err := Load(dir)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			var names []string
			for _, s := range cfg.Services {
				names = append(names, s.Name)
			}
			require.Equal(t, tc.want, names)
		})
	}
}

func Test_Load_errors(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "none.d"))
	require.ErrorContains(t, err, "read status config directory: open")
	dir := writeDir(t, nil)
	_, err = Load(dir)
	require.EqualError(t, err, "status config "+dir+": at least one service is required")
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
		"valid":          {services: []Service{valid, google, with(valid, func(s *Service) { s.Name, s.Type, s.URL = "io", TypeStatusIO, "http://io.local/status" }), with(valid, func(s *Service) { s.Name, s.Type = "dd", TypeDatadog }), with(valid, func(s *Service) { s.Name, s.Type, s.URL = "llm", TypeHTTP, "http://ai.local:8080/health" })}},
		"empty":          {errMsg: "at least one service is required"},
		"duplicate":      {services: []Service{valid, valid}, errMsg: `services[1]: duplicate name "svc-1"`},
		"bad-name":       {services: []Service{with(valid, func(s *Service) { s.Name = "Svc 1" })}, errMsg: `services[0]: name "Svc 1" must be lowercase letters, digits, and "-", and not "all"`},
		"reserved-name":  {services: []Service{with(valid, func(s *Service) { s.Name = "all" })}, errMsg: `name "all" must be`},
		"no-display":     {services: []Service{with(valid, func(s *Service) { s.Display = "" })}, errMsg: `service "svc-1": display is required`},
		"bad-type":       {services: []Service{with(valid, func(s *Service) { s.Type = "rss" })}, errMsg: `service "svc-1": type "rss" must be statuspage, statusio, slack, datadog, google, or http`},
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
		"datadog":    {service: Service{Type: TypeDatadog, URL: serveJSON(t, http.StatusOK, `{"components":[{"name":"Web","status":"degraded"}]}`)}, want: HealthDegraded},
		"http":       {service: Service{Type: TypeHTTP, URL: serveText(t, http.StatusOK, `{"status":"ok"}`)}, want: HealthOperational},
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
