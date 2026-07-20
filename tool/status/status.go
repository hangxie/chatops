// Package status checks the public status APIs of common external services.
package status

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/tool"
)

const Scheme = "status"

var ErrNilChecker = errors.New("nil service-status checker")

var sharedDefaultChecker = defaultChecker()

var healthLabels = map[Health]string{
	HealthOperational:   "OK",
	HealthMaintenance:   "MAINTENANCE",
	HealthDegraded:      "DEGRADED",
	HealthPartialOutage: "PARTIAL OUTAGE",
	HealthMajorOutage:   "MAJOR OUTAGE",
	HealthUnknown:       "UNKNOWN",
}

var displayNames = map[string]string{
	"github":     "GitHub",
	"anthropic":  "Anthropic",
	"cloudflare": "Cloudflare",
	"openai":     "OpenAI",
	"gemini":     "Google Gemini",
	"slack":      "Slack",
	"docker-hub": "Docker Hub",
}

// Opener opens the default public service-status catalog.
func Opener(ctx context.Context, u *url.URL, creds cred.Store) (tool.Tool, error) {
	return NewOpener(sharedDefaultChecker)(ctx, u, creds)
}

// NewOpener creates an opener backed by checker, primarily for explicit wiring and tests.
func NewOpener(checker *Checker) tool.OpenerFunc {
	return func(ctx context.Context, u *url.URL, _ cred.Store) (tool.Tool, error) {
		if u.Host != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" || u.User != nil || u.Fragment != "" {
			return nil, fmt.Errorf("status: URL %q takes no endpoint or configuration", u.String())
		}
		return Open(ctx, checker)
	}
}

// Tool checks external service status. It holds no resources itself.
type Tool struct {
	checker *Checker
}

// Open returns a service-status tool backed by checker.
func Open(ctx context.Context, checker *Checker) (*Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	if checker == nil {
		return nil, fmt.Errorf("status: %w", ErrNilChecker)
	}
	return &Tool{checker: checker}, nil
}

// Invoke supports "check" for one provider or all providers, and "list".
func (t *Tool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, fmt.Errorf("status: %w", err)
	}
	switch call.Action {
	case "check":
		if strings.TrimSpace(call.Target) == "" {
			return tool.Result{}, errors.New("status: check target is required")
		}
		if len(call.Parameters) != 0 {
			return tool.Result{}, errors.New("status: check takes no parameters")
		}
		snapshots, err := t.checker.Check(ctx, call.Target)
		if err != nil {
			return tool.Result{}, err
		}
		details := make(map[string]string, len(snapshots))
		for _, snapshot := range snapshots {
			details[snapshot.Provider] = string(snapshot.Health)
		}
		return tool.Result{Text: formatSnapshots(snapshots), Details: details}, nil
	case "list":
		if call.Target != "" || len(call.Parameters) != 0 {
			return tool.Result{}, errors.New("status: list takes no target or parameters")
		}
		return tool.Result{Text: "Supported services: " + strings.Join(t.checker.Names(), ", ")}, nil
	default:
		return tool.Result{}, fmt.Errorf("status: %q: %w", call.Action, tool.ErrUnknownAction)
	}
}

// Close releases nothing; HTTP resources are owned by the checker.
func (t *Tool) Close() error { return nil }

func formatSnapshots(snapshots []Snapshot) string {
	lines := make([]string, 0, len(snapshots)*3)
	for _, snapshot := range snapshots {
		lines = append(lines, fmt.Sprintf("[%s] %s — %s", healthLabel(snapshot.Health), displayName(snapshot.Provider), snapshot.Summary))
		for _, incident := range snapshot.Incidents {
			line := "  " + incident.Name
			if incident.Status != "" {
				line += " (" + incident.Status + ")"
			}
			lines = append(lines, line)
			if incident.URL != "" {
				lines = append(lines, "  "+incident.URL)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func healthLabel(health Health) string {
	return healthLabels[health]
}

func displayName(name string) string {
	if display, ok := displayNames[name]; ok {
		return display
	}
	return name
}
