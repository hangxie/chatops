package config

import (
	"fmt"
	"os"
)

// Secret reads the variable named env: "" when env is empty, an error when the variable is unset or empty.
func Secret(env string) (string, error) {
	if env == "" {
		return "", nil
	}
	value, ok := os.LookupEnv(env)
	if !ok {
		return "", fmt.Errorf("environment variable %s is not set", env)
	}
	if value == "" {
		return "", fmt.Errorf("environment variable %s is empty", env)
	}
	return value, nil
}
