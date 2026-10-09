package config

import (
	"fmt"
	"os"
)

// Secret returns the value of the environment variable named by env. An
// empty name means the secret is not configured and yields "". A named but
// unset or empty variable is an error so a misconfigured deployment fails at
// startup rather than sending unauthenticated requests.
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
