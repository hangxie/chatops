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

// The status package splits into two single-intent tools: one checks a
// named service, the other lists the checkable services.
const (
	CheckScheme = "status-check"
	ListScheme  = "status-list"
)

// serviceParam names the argument the check tool reads.
const serviceParam = "service"

// CheckDescriptor and ListDescriptor are the tools' self-descriptions for
// planners; wire each into a tool.Backend alongside its scheme and opener.
var (
	CheckDescriptor = tool.Descriptor{
		Description: "Report the current public status of one external service (GitHub, OpenAI, Slack, ...).",
		Parameters: []tool.Param{
			{
				Name:        serviceParam,
				Type:        "string",
				Required:    true,
				Description: "The service to check, e.g. github, openai, slack, cloudflare (use status-list to see all).",
			},
		},
	}
	ListDescriptor = tool.Descriptor{
		Description: "List the external services whose public status can be checked.",
	}
)

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

// CheckOpener and ListOpener open the check and list tools against the
// default public service-status catalog.
func CheckOpener(ctx context.Context, u *url.URL, creds cred.Store) (tool.Tool, error) {
	return NewCheckOpener(sharedDefaultChecker)(ctx, u, creds)
}

func ListOpener(ctx context.Context, u *url.URL, creds cred.Store) (tool.Tool, error) {
	return NewListOpener(sharedDefaultChecker)(ctx, u, creds)
}

// NewCheckOpener and NewListOpener create openers backed by checker,
// primarily for explicit wiring and tests.
func NewCheckOpener(checker *Checker) tool.OpenerFunc {
	return openerFor(checker, func(c *Checker) tool.Tool { return &checkTool{checker: c} })
}

func NewListOpener(checker *Checker) tool.OpenerFunc {
	return openerFor(checker, func(c *Checker) tool.Tool { return &listTool{checker: c} })
}

// openerFor builds an opener that rejects any endpoint or configuration in
// the URL and constructs a single-intent tool via build.
func openerFor(checker *Checker, build func(*Checker) tool.Tool) tool.OpenerFunc {
	return func(ctx context.Context, u *url.URL, _ cred.Store) (tool.Tool, error) {
		if u.Host != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" || u.User != nil || u.Fragment != "" {
			return nil, fmt.Errorf("status: URL %q takes no endpoint or configuration", u.String())
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("status: %w", err)
		}
		if checker == nil {
			return nil, fmt.Errorf("status: %w", ErrNilChecker)
		}
		return build(checker), nil
	}
}

// checkTool reports the status of one named service.
type checkTool struct {
	checker *Checker
}

// Invoke checks the service named by call.Arguments["service"].
func (t *checkTool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, fmt.Errorf("status: %w", err)
	}
	service := strings.TrimSpace(call.Arguments[serviceParam])
	if service == "" {
		return tool.Result{}, errors.New("status: check requires a service")
	}
	snapshots, err := t.checker.Check(ctx, service)
	if err != nil {
		return tool.Result{}, err
	}
	details := make(map[string]string, len(snapshots))
	for _, snapshot := range snapshots {
		details[snapshot.Provider] = string(snapshot.Health)
	}
	return tool.Result{Text: formatSnapshots(snapshots), Details: details}, nil
}

// Close releases nothing; HTTP resources are owned by the checker.
func (t *checkTool) Close() error { return nil }

// listTool lists the services whose status can be checked.
type listTool struct {
	checker *Checker
}

// Invoke lists the checkable services. Call.Arguments is ignored.
func (t *listTool) Invoke(ctx context.Context, _ tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, fmt.Errorf("status: %w", err)
	}
	return tool.Result{Text: "Supported services: " + strings.Join(t.checker.Names(), ", ")}, nil
}

// Close releases nothing; HTTP resources are owned by the checker.
func (t *listTool) Close() error { return nil }

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
