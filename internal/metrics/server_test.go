package metrics

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_Serve(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, New("v9")) }()

	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + "/metrics")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `chatops_build_info{version="v9"} 1`)

	for path, code := range map[string]int{"/healthz": http.StatusOK, "/readyz": http.StatusServiceUnavailable, "/other": http.StatusNotFound} {
		resp, err = http.Get(base + path)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, code, resp.StatusCode, path)
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func Test_Serve_closed_listener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, ln.Close())
	require.Error(t, Serve(context.Background(), ln, New("v0")))
}
