package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Health is a service's normalized health.
type Health string

const (
	HealthOperational   Health = "operational"
	HealthMaintenance   Health = "maintenance"
	HealthDegraded      Health = "degraded"
	HealthPartialOutage Health = "partial_outage"
	HealthMajorOutage   Health = "major_outage"
	HealthUnknown       Health = "unknown"
)

const (
	allServices         = "all"
	requestTimeout      = 5 * time.Second
	maxConcurrentChecks = 4
	maxResponseBytes    = 4 << 20
)

var healthRank = map[Health]int{
	HealthOperational:   0,
	HealthMaintenance:   1,
	HealthDegraded:      2,
	HealthPartialOutage: 3,
	HealthMajorOutage:   4,
	HealthUnknown:       5,
}

// Incident is one active incident a status page reports.
type Incident struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
	URL    string `json:"url,omitempty"`
}

// Snapshot is one service's current status.
type Snapshot struct {
	Service   string     `json:"service"`
	Health    Health     `json:"health" jsonschema:"operational, maintenance, degraded, partial_outage, major_outage, or unknown"`
	Summary   string     `json:"summary"`
	Incidents []Incident `json:"incidents,omitempty"`
}

// checkFunc reads one status page; Service is filled in by the checker.
type checkFunc func(ctx context.Context, client *http.Client) (Snapshot, error)

type service struct {
	name    string
	display string
	covers  []string
	check   checkFunc
}

type checker struct {
	client   *http.Client
	services []service
}

func newChecker(cfg Config) (*checker, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	c := &checker{client: &http.Client{Timeout: requestTimeout}}
	for _, s := range cfg.Services {
		c.services = append(c.services, service{name: s.Name, display: s.Display, covers: s.Covers, check: s.check()})
	}
	return c, nil
}

// check checks the named service, or every service concurrently when name is "all".
func (c *checker) check(ctx context.Context, name string) ([]Snapshot, error) {
	targets := c.services
	if name != allServices {
		i := slices.IndexFunc(c.services, func(s service) bool { return s.name == name })
		if i < 0 {
			return nil, fmt.Errorf("unknown service %q", name)
		}
		targets = c.services[i : i+1]
	}
	snapshots := make([]Snapshot, len(targets))
	slots := make(chan struct{}, maxConcurrentChecks)
	var wg sync.WaitGroup
	for i, s := range targets {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			snapshots[i] = c.checkOne(ctx, s)
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return snapshots, nil
}

// checkOne reports a status page it cannot read as unknown health rather than failing the call.
func (c *checker) checkOne(ctx context.Context, s service) Snapshot {
	snapshot, err := s.check(ctx, c.client)
	if err != nil {
		snapshot = Snapshot{Health: HealthUnknown, Summary: "unable to check: " + err.Error()}
	}
	snapshot.Service = s.display
	return snapshot
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, destination any) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "chatops-mcp-status")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request status: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close status response: %w", closeErr))
		}
	}()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("request status: HTTP %s", response.Status)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(destination); err != nil {
		return fmt.Errorf("decode status response: %w", err)
	}
	return nil
}

func worstHealth(left, right Health) Health {
	if healthRank[right] > healthRank[left] {
		return right
	}
	return left
}

// activeSummary describes a service with incidents, or none.
func activeSummary(subject string, incidents int) string {
	switch incidents {
	case 0:
		return "All Systems Operational"
	case 1:
		return "Active " + subject + "incident"
	}
	return "Active " + subject + "incidents"
}
