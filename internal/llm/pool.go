package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hangxie/chatops/internal/config"
)

// Model completes one model request.
type Model interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Pool picks each turn's first model by schedule and fails a call over to the next model; it is safe for concurrent turns.
type Pool struct {
	models   []Model
	names    []string
	schedule string
	tries    int
	logger   *slog.Logger
	// next is the round_robin counter, or the sticky model that last answered.
	next atomic.Uint64
}

// NewPool builds a client per configured model; a nil logger uses slog.Default.
func NewPool(cfg config.LLM, httpClient *http.Client, logger *slog.Logger) (*Pool, error) {
	models := make([]Model, len(cfg.Models))
	names := make([]string, len(cfg.Models))
	for i, model := range cfg.Models {
		client, err := New(model, httpClient)
		if err != nil {
			return nil, fmt.Errorf("llm.models[%d]: %w", i, err)
		}
		models[i], names[i] = client, model.Name
	}
	if logger == nil {
		logger = slog.Default()
	}
	return newPool(models, names, cfg.Schedule, cfg.MaxModelRetry, logger), nil
}

func newPool(models []Model, names []string, schedule string, tries int, logger *slog.Logger) *Pool {
	return &Pool{models: models, names: names, schedule: schedule, tries: tries, logger: logger}
}

// Session starts a turn on the model the schedule picks; the turn keeps whichever model last answered.
func (p *Pool) Session() Model {
	start := 0
	switch p.schedule {
	case config.ScheduleRoundRobin:
		start = int((p.next.Add(1) - 1) % uint64(len(p.models)))
	case config.ScheduleSticky:
		start = int(p.next.Load())
	}
	return &session{pool: p, current: start}
}

// session is one turn's view of the pool; a turn's model calls are sequential.
type session struct {
	pool    *Pool
	current int
}

func (s *session) Complete(ctx context.Context, req Request) (Response, error) {
	p := s.pool
	var attempts []Attempt
	for try := 1; ; try++ {
		start := time.Now()
		resp, err := p.models[s.current].Complete(ctx, req)
		attempts = append(attempts, Attempt{Model: p.names[s.current], Duration: time.Since(start), Err: err})
		resp.Model, resp.Attempts = p.names[s.current], attempts
		if err == nil {
			if p.schedule == config.ScheduleSticky {
				p.next.Store(uint64(s.current))
			}
			return resp, nil
		}
		if try >= p.tries || ctx.Err() != nil || !errors.Is(err, ErrUnavailable) {
			return resp, err
		}
		failed := p.names[s.current]
		s.current = (s.current + 1) % len(p.models)
		p.logger.Warn("model failed over", "model", failed, "next", p.names[s.current], "error", err)
	}
}
