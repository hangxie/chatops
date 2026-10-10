package serve

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

type bearerRoundTripper string

func (token bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+string(token))
	return http.DefaultTransport.RoundTrip(req)
}

func connectHTTP(endpoint, token string) (*mcp.ClientSession, error) {
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint, MaxRetries: -1}
	if token != "" {
		transport.HTTPClient = &http.Client{Transport: bearerRoundTripper(token)}
	}
	return newClient().Connect(context.Background(), transport, nil)
}

func Test_bearerToken(t *testing.T) {
	tests := map[string]struct {
		env    string
		value  *string
		token  string
		errMsg string
	}{
		"no-env": {env: "", token: ""},
		"set":    {env: "SERVE_TEST_TOKEN", value: new("s3cret"), token: "s3cret"},
		"empty":  {env: "SERVE_TEST_TOKEN", value: new(""), errMsg: "environment variable SERVE_TEST_TOKEN is unset or empty"},
		"unset":  {env: "SERVE_TEST_TOKEN_UNSET", errMsg: "environment variable SERVE_TEST_TOKEN_UNSET is unset or empty"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if tc.value != nil {
				t.Setenv(tc.env, *tc.value)
			}
			token, err := bearerToken(tc.env)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.token, token)
		})
	}
}

func Test_checkBind(t *testing.T) {
	tests := map[string]struct {
		addr   string
		token  string
		errMsg string
	}{
		"ipv4-loopback":     {addr: "127.0.0.1:8080"},
		"ipv4-loopback-net": {addr: "127.1.2.3:8080"},
		"ipv6-loopback":     {addr: "[::1]:8080"},
		"localhost":         {addr: "localhost:8080"},
		"all-interfaces":    {addr: ":8080", errMsg: `refusing to serve http on non-loopback address ":8080" without --token-env`},
		"unspecified":       {addr: "0.0.0.0:8080", errMsg: `refusing to serve http on non-loopback address "0.0.0.0:8080" without --token-env`},
		"lan":               {addr: "192.168.0.10:8080", errMsg: `refusing to serve http on non-loopback address "192.168.0.10:8080" without --token-env`},
		"hostname":          {addr: "example.internal:8080", errMsg: `refusing to serve http on non-loopback address "example.internal:8080" without --token-env`},
		"lan-with-token":    {addr: "0.0.0.0:8080", token: "s3cret"},
		"malformed":         {addr: "no-port", errMsg: "invalid --http address: address no-port: missing port in address"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := checkBind(tc.addr, tc.token)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
		})
	}
}

func Test_httpHandler(t *testing.T) {
	tests := map[string]struct {
		serverToken string
		clientToken string
		status      int
	}{
		"open":          {status: http.StatusOK},
		"token-ok":      {serverToken: "s3cret", clientToken: "s3cret", status: http.StatusOK},
		"token-missing": {serverToken: "s3cret", status: http.StatusUnauthorized},
		"token-wrong":   {serverToken: "s3cret", clientToken: "guess", status: http.StatusUnauthorized},
		"token-prefix":  {serverToken: "s3cret", clientToken: "s3cre", status: http.StatusUnauthorized},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			server, err := newServer(Cmd{Tools: []string{"ping"}})
			require.NoError(t, err)
			httpServer := httptest.NewServer(httpHandler(server, tc.serverToken))
			defer httpServer.Close()

			session, err := connectHTTP(httpServer.URL+"/mcp", tc.clientToken)
			if tc.status != http.StatusOK {
				require.ErrorContains(t, err, "Unauthorized")
				return
			}
			require.NoError(t, err)
			defer func() { _ = session.Close() }()
			require.Equal(t, []string{"ping"}, toolNames(t, session))
		})
	}
}

func Test_serveHTTP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server, err := newServer(Cmd{Tools: []string{"ping"}})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, listener, httpHandler(server, "")) }()

	session, err := connectHTTP("http://"+listener.Addr().String()+"/mcp", "")
	require.NoError(t, err)
	require.Equal(t, []string{"ping"}, toolNames(t, session))
	require.NoError(t, session.Close())
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func Test_serveHTTP_closed_listener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, listener.Close())

	err = serveHTTP(context.Background(), listener, http.NotFoundHandler())
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "serve http: "), err.Error())
}
