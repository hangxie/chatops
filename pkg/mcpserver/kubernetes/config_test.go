package kubernetes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// packagedConfig is the directory the packages install as /etc/chatops/mcp-kubernetes.d.
const packagedConfig = "../../../package/systemd/mcp-kubernetes.d"

// writeDir writes files into a new config directory and returns it.
func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	return dir
}

func Test_Load(t *testing.T) {
	relative := writeDir(t, map[string]string{"00-default.yaml": "kubeconfig: kube/config\n"})
	merged := writeDir(t, map[string]string{
		"00-default.yaml": "kubeconfig: /etc/chatops/kubeconfig\nmax_results: 50\n",
		"10-site.yaml":    "kubeconfig: site/config\nmax_results: 100\n",
		"20-context.yaml": "context: homelab\n",
	})
	tests := map[string]struct {
		dir  string
		want Config
	}{
		"packaged":                            {dir: packagedConfig, want: Config{Kubeconfig: "/etc/chatops/kubeconfig", MaxResults: 50}},
		"empty dir":                           {dir: writeDir(t, nil), want: Config{}},
		"empty file":                          {dir: writeDir(t, map[string]string{"00-default.yaml": ""}), want: Config{}},
		"all fields":                          {dir: writeDir(t, map[string]string{"a.yaml": "kubeconfig: /k/config\ncontext: homelab\nmax_results: 20\n"}), want: Config{Kubeconfig: "/k/config", Context: "homelab", MaxResults: 20}},
		"relative kubeconfig follows the dir": {dir: relative, want: Config{Kubeconfig: filepath.Join(relative, "kube/config")}},
		"later files override":                {dir: merged, want: Config{Kubeconfig: filepath.Join(merged, "site/config"), Context: "homelab", MaxResults: 100}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(tc.dir)
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg)
		})
	}
}

func Test_Load_errors(t *testing.T) {
	tests := map[string]struct {
		dir    string
		errMsg string
	}{
		"missing":       {dir: filepath.Join(t.TempDir(), "none.d"), errMsg: "read kubernetes config directory: open"},
		"bad-yaml":      {dir: writeDir(t, map[string]string{"a.yaml": "kubeconfig: [\n"}), errMsg: "parse kubernetes config"},
		"unknown-field": {dir: writeDir(t, map[string]string{"a.yaml": "kubeconfig: /k\nnamespace: x\n"}), errMsg: "field namespace not found"},
		"zero-max":      {dir: writeDir(t, map[string]string{"a.yaml": "max_results: 0\n"}), errMsg: "max_results must be at least 1, got 0"},
		"negative-max":  {dir: writeDir(t, map[string]string{"a.yaml": "max_results: -5\n"}), errMsg: "max_results must be at least 1, got -5"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(tc.dir)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}
