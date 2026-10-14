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
	if cfg.Kubeconfig != "" && !filepath.IsAbs(cfg.Kubeconfig) {
		cfg.Kubeconfig = filepath.Join(filepath.Dir(path), cfg.Kubeconfig)
	}
	return cfg, nil
}
