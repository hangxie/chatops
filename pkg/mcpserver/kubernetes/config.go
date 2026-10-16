package kubernetes

import (
	"fmt"
	"path/filepath"

	"github.com/hangxie/chatops/internal/confdir"
)

// configFile is the schema of each file in the --config directory; an omitted field keeps its default.
type configFile struct {
	Kubeconfig string `yaml:"kubeconfig"`
	Context    string `yaml:"context"`
	// MaxResults is a pointer so an explicit 0 is rejected rather than read as the default.
	MaxResults *int `yaml:"max_results"`
}

// Load merges the *.yaml files in a kubernetes config directory; unknown fields are errors, and a relative
// kubeconfig is resolved against that directory.
func Load(dir string) (Config, error) {
	var file configFile
	if err := confdir.Decode("kubernetes", dir, &file); err != nil {
		return Config{}, err
	}
	cfg := Config{Kubeconfig: file.Kubeconfig, Context: file.Context}
	if file.MaxResults != nil {
		if *file.MaxResults < 1 {
			return Config{}, fmt.Errorf("kubernetes config %s: max_results must be at least 1, got %d", dir, *file.MaxResults)
		}
		cfg.MaxResults = *file.MaxResults
	}
	if cfg.Kubeconfig != "" && !filepath.IsAbs(cfg.Kubeconfig) {
		cfg.Kubeconfig = filepath.Join(dir, cfg.Kubeconfig)
	}
	return cfg, nil
}
