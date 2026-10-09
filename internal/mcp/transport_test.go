package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/pkg/mcpserver/ping"
)

// stdioServerEnv makes the re-executed test binary a stdio ping server.
const stdioServerEnv = "CHATOPS_MCP_TEST_STDIO_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(stdioServerEnv) == "1" {
		server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stdio-test", Version: "v0"}, nil)
		ping.Register(server)
		if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func stdioTestServer() config.Server {
	return config.Server{
		Transport: config.TransportStdio,
		Command:   os.Args[0],
		Args:      []string{"-test.run=^$"},
		Env:       map[string]string{stdioServerEnv: "1"},
	}
}

func Test_commandEnv(t *testing.T) {
	t.Setenv("PATH", "/bin")
	t.Setenv("HOME", "/home/x")
	t.Setenv("SLACK_BOT_TOKEN", "secret")
	t.Setenv("CHATOPS_TEST_VAULT_TOKEN", "vault")

	tests := map[string]struct {
		server config.Server
		env    []string
		errMsg string
	}{
		"inherited-only": {env: []string{"PATH=/bin", "HOME=/home/x"}},
		"plain": {
			server: config.Server{Env: map[string]string{"B": "2", "A": "1", "HOME": "/srv"}},
			env:    []string{"PATH=/bin", "A=1", "B=2", "HOME=/srv"},
		},
		"secret": {
			server: config.Server{Env: map[string]string{"A": "1"}, SecretEnv: map[string]string{"VAULT_TOKEN": "CHATOPS_TEST_VAULT_TOKEN"}},
			env:    []string{"PATH=/bin", "HOME=/home/x", "A=1", "VAULT_TOKEN=vault"},
		},
		"secret-overrides-inherited": {
			server: config.Server{SecretEnv: map[string]string{"HOME": "CHATOPS_TEST_VAULT_TOKEN"}},
			env:    []string{"PATH=/bin", "HOME=vault"},
		},
		"secret-unset": {
			server: config.Server{SecretEnv: map[string]string{"VAULT_TOKEN": "CHATOPS_TEST_MISSING_SECRET"}},
			errMsg: "secret_env.VAULT_TOKEN: environment variable CHATOPS_TEST_MISSING_SECRET",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			env, err := commandEnv(tc.server)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.env, env)
		})
	}
}

func Test_commandEnv_unset_parent(t *testing.T) {
	t.Setenv("PATH", "")
	require.NoError(t, os.Unsetenv("PATH"))
	t.Setenv("HOME", "")
	require.NoError(t, os.Unsetenv("HOME"))
	env, err := commandEnv(config.Server{})
	require.NoError(t, err)
	require.Empty(t, env)
}

func Test_newTransport_stdio(t *testing.T) {
	transport, err := newTransport(stdioTestServer())
	require.NoError(t, err)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "c", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()
	require.Equal(t, "stdio-test", session.InitializeResult().ServerInfo.Name)
}

func Test_newTransport_http_bearer(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http-test", Version: "v0"}, nil)
	ping.Register(server)
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)

	var sawAuth []string
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = append(sawAuth, r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	t.Setenv("CHATOPS_TEST_MCP_TOKEN", "tok")
	transport, err := newTransport(config.Server{Transport: config.TransportStreamableHTTP, URL: httpServer.URL, BearerTokenEnv: "CHATOPS_TEST_MCP_TOKEN"})
	require.NoError(t, err)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "c", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	require.NoError(t, session.Close())
	require.NotEmpty(t, sawAuth)
	for _, auth := range sawAuth {
		require.Equal(t, "Bearer tok", auth)
	}
}

func Test_newTransport_http_bearer_no_redirect(t *testing.T) {
	var targetRequests, targetAuth []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests = append(targetRequests, r.URL.Path)
		targetAuth = append(targetAuth, r.Header.Get("Authorization"))
		http.Error(w, "unexpected", http.StatusTeapot)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	t.Setenv("CHATOPS_TEST_MCP_TOKEN", "tok")
	transport, err := newTransport(config.Server{Transport: config.TransportStreamableHTTP, URL: redirector.URL + "/mcp", BearerTokenEnv: "CHATOPS_TEST_MCP_TOKEN"})
	require.NoError(t, err)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "c", Version: "v0"}, nil)
	_, err = client.Connect(context.Background(), transport, nil)
	require.ErrorContains(t, err, "refusing to follow redirect")
	require.Empty(t, targetRequests, "redirect target must not be contacted")
	require.Empty(t, targetAuth)
}

func Test_newTransport_errors(t *testing.T) {
	tests := map[string]struct {
		server config.Server
		errMsg string
	}{
		"missing-token": {
			server: config.Server{Transport: config.TransportStreamableHTTP, URL: "http://h/mcp", BearerTokenEnv: "CHATOPS_TEST_MISSING_TOKEN"},
			errMsg: "bearer token: environment variable CHATOPS_TEST_MISSING_TOKEN is not set",
		},
		"missing-secret-env": {
			server: config.Server{Transport: config.TransportStdio, Command: "x", SecretEnv: map[string]string{"VAULT_TOKEN": "CHATOPS_TEST_MISSING_SECRET"}},
			errMsg: "secret_env.VAULT_TOKEN: environment variable CHATOPS_TEST_MISSING_SECRET is not set",
		},
		"unknown-transport": {
			server: config.Server{Transport: "sse"},
			errMsg: `unsupported transport "sse"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newTransport(tc.server)
			require.EqualError(t, err, tc.errMsg)
		})
	}
}

func Test_newTransport_http_no_token(t *testing.T) {
	transport, err := newTransport(config.Server{Transport: config.TransportStreamableHTTP, URL: "http://h/mcp"})
	require.NoError(t, err)
	streamable, ok := transport.(*mcpsdk.StreamableClientTransport)
	require.True(t, ok)
	require.Equal(t, "http://h/mcp", streamable.Endpoint)
	require.Nil(t, streamable.HTTPClient)
}
