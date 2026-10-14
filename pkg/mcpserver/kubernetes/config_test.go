package kubernetes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// packagedConfig is the file the packages install as /etc/chatops/mcp-kubernetes.yaml.
const packagedConfig = "../../../package/systemd/mcp-kubernetes.yaml"

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp-kubernetes.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func Test_Load(t *testing.T) {
	dir := t.TempDir()
	relative := filepath.Join(dir, "mcp-kubernetes.yaml")
	require.NoError(t, os.WriteFile(relative, []byte("kubeconfig: kube/config\n"), 0o600))
	tests := map[string]struct {
		path string
		want Config
	}{
		"packaged":                             {path: packagedConfig, want: Config{Kubeconfig: "/etc/chatops/kubeconfig", MaxResults: 50}},
		"empty file":                           {path: writeFile(t, ""), want: Config{}},
		"all fields":                           {path: writeFile(t, "kubeconfig: /k/config\ncontext: homelab\nmax_results: 20\n"), want: Config{Kubeconfig: "/k/config", Context: "homelab", MaxResults: 20}},
		"relative kubeconfig follows the file": {path: relative, want: Config{Kubeconfig: filepath.Join(dir, "kube/config")}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(tc.path)
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg)
		})
	}
}

func Test_Load_errors(t *testing.T) {
	tests := map[string]struct {
		path   string
		errMsg string
	}{
		"missing":       {path: filepath.Join(t.TempDir(), "none.yaml"), errMsg: "read kubernetes config: open"},
		"bad-yaml":      {path: writeFile(t, "kubeconfig: [\n"), errMsg: "parse kubernetes config"},
		"unknown-field": {path: writeFile(t, "kubeconfig: /k\nnamespace: x\n"), errMsg: "field namespace not found"},
		"zero-max":      {path: writeFile(t, "max_results: 0\n"), errMsg: "max_results must be at least 1, got 0"},
		"negative-max":  {path: writeFile(t, "max_results: -5\n"), errMsg: "max_results must be at least 1, got -5"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(tc.path)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}
