package metrics

import (
	"fmt"
	"net/http"
	"strings"
)

// Live answers 200 while the process can serve HTTP; it checks nothing else so a liveness probe never restarts a busy daemon.
func (m *Metrics) Live() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { respond(w, nil) })
}

// Ready answers 200 once Watch has wired the sources, the manager accepts questions, and every chat backend and MCP server is up.
func (m *Metrics) Ready() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s := m.sources.Load()
		if s == nil {
			respond(w, []string{"starting"})
			return
		}
		var failures []string
		if !s.Accepting() {
			failures = append(failures, "shutting down")
		}
		for _, backend := range s.Chat() {
			if !backend.Up {
				failures = append(failures, fmt.Sprintf("chat backend %s: not connected", backend.Name))
			}
		}
		for _, server := range s.MCP() {
			if !server.Up {
				failures = append(failures, fmt.Sprintf("mcp server %s: down", server.ID))
			}
		}
		respond(w, failures)
	})
}

func respond(w http.ResponseWriter, failures []string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if len(failures) == 0 {
		_, _ = fmt.Fprintln(w, "ok")
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintln(w, strings.Join(failures, "\n"))
}
