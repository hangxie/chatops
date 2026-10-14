package kubernetes

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// configFile is the --kubernetes-config schema; an omitted field keeps its default.
type configFile struct {
	Kubeconfig string `yaml:"kubeconfig"`
	Context    string `yaml:"context"`
	// MaxResults is a pointer so an explicit 0 is rejected rather than read as the default.
	MaxResults *int `yaml:"max_results"`
}

// Load reads a kubernetes config file; unknown fields are errors, and a relative kubeconfig is resolved
// against the file's directory.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read kubernetes config: %w", err)
	}
	var file configFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse kubernetes config %s: %w", path, err)
	}
	cfg := Config{Kubeconfig: file.Kubeconfig, Context: file.Context}
	if file.MaxResults != nil {
		if *file.MaxResults < 1 {
			return Config{}, fmt.Errorf("kubernetes config %s: max_results must be at least 1, got %d", path, *file.MaxResults)
		}
		cfg.MaxResults = *file.MaxResults
	}
	if cfg.Kubeconfig != "" && !filepath.IsAbs(cfg.Kubeconfig) {
		cfg.Kubeconfig = filepath.Join(filepath.Dir(path), cfg.Kubeconfig)
	}
	return cfg, nil
}
