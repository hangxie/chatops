package mcp

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/internal/config"
)

// inheritedEnv is all a stdio server inherits from the daemon, so daemon secrets never leak.
var inheritedEnv = []string{"PATH", "HOME"}

func newTransport(server config.Server) (mcpsdk.Transport, error) {
	switch server.Transport {
	case config.TransportStdio:
		env, err := commandEnv(server)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(server.Command, server.Args...)
		cmd.Env = env
		cmd.Stderr = os.Stderr
		return &mcpsdk.CommandTransport{Command: cmd}, nil
	case config.TransportStreamableHTTP:
		token, err := config.Secret(server.BearerTokenEnv)
		if err != nil {
			return nil, fmt.Errorf("bearer token: %w", err)
		}
		transport := &mcpsdk.StreamableClientTransport{Endpoint: server.URL}
		if token != "" {
			transport.HTTPClient = &http.Client{
				Transport:     bearerTransport{token: token, base: http.DefaultTransport},
				CheckRedirect: refuseRedirect,
			}
		}
		return transport, nil
	}
	return nil, fmt.Errorf("unsupported transport %q", server.Transport)
}

// commandEnv builds the child environment from inheritedEnv, env, and resolved secret_env.
func commandEnv(server config.Server) ([]string, error) {
	configured := make(map[string]string, len(server.Env)+len(server.SecretEnv))
	maps.Copy(configured, server.Env)
	for name, source := range server.SecretEnv {
		value, err := config.Secret(source)
		if err != nil {
			return nil, fmt.Errorf("secret_env.%s: %w", name, err)
		}
		configured[name] = value
	}

	env := []string{}
	for _, name := range inheritedEnv {
		if _, override := configured[name]; override {
			continue
		}
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(configured)) {
		env = append(env, name+"="+configured[name])
	}
	return env, nil
}

// refuseRedirect keeps bearerTransport from sending the token to a redirect target.
func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("refusing to follow redirect to %s for an authenticated MCP server", req.URL.Redacted())
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}
