package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnknownProvider identifies a target that is not in the provider catalog.
var ErrUnknownProvider = errors.New("unknown service-status provider")

// Health is the normalized health of one service.
type Health string

const (
	HealthOperational   Health = "operational"
	HealthMaintenance   Health = "maintenance"
	HealthDegraded      Health = "degraded"
	HealthPartialOutage Health = "partial_outage"
	HealthMajorOutage   Health = "major_outage"
	HealthUnknown       Health = "unknown"
)

// Incident is one active incident reported by a provider.
type Incident struct {
	Name   string
	Status string
	URL    string
}

// Snapshot is the normalized current state of one provider.
type Snapshot struct {
	Provider  string
	Health    Health
	Summary   string
	Incidents []Incident
}

// Provider fetches current status from one public status API.
type Provider interface {
	Name() string
	Aliases() []string
	Check(ctx context.Context) (Snapshot, error)
}

// Checker resolves provider targets and performs status checks.
type Checker struct {
	providers map[string]Provider
	aliases   map[string]string
	names     []string
}

const maxConcurrentChecks = 4

var healthRank = map[Health]int{
	HealthOperational:   0,
	HealthMaintenance:   1,
	HealthDegraded:      2,
	HealthPartialOutage: 3,
	HealthMajorOutage:   4,
	HealthUnknown:       5,
}

// NewChecker validates providers and builds an immutable provider catalog.
func NewChecker(providers []Provider) (*Checker, error) {
	checker := &Checker{providers: make(map[string]Provider, len(providers)), aliases: map[string]string{}}
	for _, provider := range providers {
		if provider == nil {
			return nil, errors.New("status: nil provider")
		}
		name := normalizeName(provider.Name())
		if name == "" || name == "all" {
			return nil, fmt.Errorf("status: invalid provider name %q", provider.Name())
		}
		if _, exists := checker.providers[name]; exists {
			return nil, fmt.Errorf("status: duplicate provider name %q", name)
		}
		if _, exists := checker.aliases[name]; exists {
			return nil, fmt.Errorf("status: provider name %q conflicts with alias", name)
		}
		checker.providers[name] = provider
		checker.names = append(checker.names, name)
		for _, rawAlias := range provider.Aliases() {
			alias := normalizeName(rawAlias)
			if alias == "" || alias == "all" {
				return nil, fmt.Errorf("status: invalid alias %q", rawAlias)
			}
			if _, exists := checker.providers[alias]; exists {
				return nil, fmt.Errorf("status: alias %q conflicts with provider", alias)
			}
			if _, exists := checker.aliases[alias]; exists {
				return nil, fmt.Errorf("status: duplicate alias %q", alias)
			}
			checker.aliases[alias] = name
		}
	}
	return checker, nil
}

// Names returns canonical provider names in catalog order.
func (c *Checker) Names() []string {
	return append([]string(nil), c.names...)
}

// Check checks one provider or every provider when target is "all".
func (c *Checker) Check(ctx context.Context, target string) ([]Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target = normalizeName(target)
	if target != "all" {
		name, provider, err := c.resolve(target)
		if err != nil {
			return nil, err
		}
		snapshot, err := checkProvider(ctx, name, provider)
		if err != nil {
			return nil, err
		}
		return []Snapshot{snapshot}, nil
	}

	type checked struct {
		name     string
		snapshot Snapshot
		err      error
	}
	results := make(chan checked, len(c.names))
	semaphore := make(chan struct{}, maxConcurrentChecks)
	for _, name := range c.names {
		provider := c.providers[name]
		go func() {
			semaphore <- struct{}{}
			snapshot, err := checkProvider(ctx, name, provider)
			<-semaphore
			results <- checked{name: name, snapshot: snapshot, err: err}
		}()
	}
	byName := make(map[string]Snapshot, len(c.names))
	for range c.names {
		result := <-results
		if result.err != nil {
			return nil, result.err
		}
		byName[result.name] = result.snapshot
	}
	snapshots := make([]Snapshot, 0, len(c.names))
	for _, name := range c.names {
		snapshots = append(snapshots, byName[name])
	}
	return snapshots, nil
}

func (c *Checker) resolve(target string) (string, Provider, error) {
	if canonical, ok := c.aliases[target]; ok {
		target = canonical
	}
	provider, ok := c.providers[target]
	if !ok {
		return "", nil, fmt.Errorf("status: %q: %w", target, ErrUnknownProvider)
	}
	return target, provider, nil
}

func checkProvider(ctx context.Context, name string, provider Provider) (Snapshot, error) {
	snapshot, err := provider.Check(ctx)
	if err == nil {
		snapshot.Provider = name
		return snapshot, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Snapshot{}, ctxErr
	}
	return Snapshot{Provider: name, Health: HealthUnknown, Summary: "Unable to check: " + err.Error()}, nil
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func defaultChecker() *Checker {
	client := &http.Client{Timeout: 5 * time.Second}
	return mustChecker(NewChecker([]Provider{
		NewStatuspageProvider("github", []string{"gh"}, "https://www.githubstatus.com/api/v2/summary.json", client),
		NewStatuspageProvider("anthropic", []string{"claude"}, "https://status.anthropic.com/api/v2/summary.json", client),
		NewStatuspageProvider("cloudflare", []string{"cf"}, "https://www.cloudflarestatus.com/api/v2/summary.json", client),
		NewStatuspageProvider("openai", nil, "https://status.openai.com/api/v2/summary.json", client),
		NewGoogleProvider("https://status.cloud.google.com/incidents.json", "https://www.google.com/appsstatus/dashboard/incidents.json", client),
		NewSlackProvider("https://slack-status.com/api/v2.0.0/current", client),
		NewStatusIOProvider("docker-hub", []string{"docker", "dockerhub"}, "https://api.status.io/1.0/status/533c6539221ae15e3f000031", client),
	}))
}

func mustChecker(checker *Checker, err error) *Checker {
	if err != nil {
		panic(err)
	}
	return checker
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, destination any) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "chatops-service-status")
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
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(destination); err != nil {
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
