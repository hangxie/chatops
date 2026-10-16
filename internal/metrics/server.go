package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// Serve answers /metrics, /healthz, and /readyz on ln until ctx ends, then shuts down; it returns nil after a clean shutdown.
func Serve(ctx context.Context, ln net.Listener, m *Metrics) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	mux.Handle("/healthz", m.Live())
	mux.Handle("/readyz", m.Ready())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}

	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := server.Shutdown(shutdownCtx)
	if serveErr := <-done; !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}
