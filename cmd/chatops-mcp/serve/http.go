package serve

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const shutdownTimeout = 5 * time.Second

// bearerToken reads the token from the named variable; no name means no token.
func bearerToken(env string) (string, error) {
	if env == "" {
		return "", nil
	}
	token := os.Getenv(env)
	if token == "" {
		return "", fmt.Errorf("environment variable %s is unset or empty", env)
	}
	return token, nil
}

// checkBind refuses to expose an unauthenticated endpoint beyond loopback.
func checkBind(addr, token string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --http address: %w", err)
	}
	if token != "" || host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to serve http on non-loopback address %q without --token-env", addr)
}

// httpHandler mounts the server at /mcp, requiring the bearer token when one is set.
func httpHandler(server *mcp.Server, token string) http.Handler {
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	if token != "" {
		verify := func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				return nil, auth.ErrInvalidToken
			}
			return &auth.TokenInfo{}, nil
		}
		handler = auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(handler)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	return mux
}

func serveHTTP(ctx context.Context, listener net.Listener, handler http.Handler) error {
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()

	select {
	case err := <-errs:
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down http: %w", err)
	}
	if err := <-errs; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}
