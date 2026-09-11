// Package status checks the public status APIs of common external services
// and exposes them as MCP tools.
//
// The package exports GroupName and Register for wiring its tools into an
// mcpserve.Registry. The group registers two single-intent tools:
//
//	status-check  report the status of one named service
//	status-list   list the services whose status can be checked
//
// Upstream status endpoints live in the compiled provider catalog rather than
// in configuration, so tool arguments cannot turn the group into an arbitrary
// HTTP client. The group takes no options and no credentials.
package status

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/mcpserve"
)

// GroupName is the built-in group this package registers into.
const GroupName = "status"

// Model-facing tool names.
const (
	CheckToolName = "status-check"
	ListToolName  = "status-list"
)

// ErrNilChecker reports a registration with no provider catalog.
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

// CheckArgs is the input schema of the status-check tool.
type CheckArgs struct {
	Service string `json:"service" jsonschema:"The service to check, e.g. github, openai, slack, cloudflare; use status-list to see all, or all to check every service."`
}

// ListArgs is the input schema of the status-list tool: it reads nothing.
type ListArgs struct{}

// Register adds the status tools to s, backed by the default public
// service-status catalog. It takes no options and no credentials.
func Register(s *mcp.Server, opts mcpserve.Options) error {
	if err := mcpserve.CheckOptions(GroupName, opts.Query); err != nil {
		return err
	}
	return RegisterChecker(s, sharedDefaultChecker.withLogger(opts.Logger))
}

// RegisterChecker adds the status tools to s backed by checker, for explicit
// wiring and tests.
func RegisterChecker(s *mcp.Server, checker *Checker) error {
	if checker == nil {
		return fmt.Errorf("status: %w", ErrNilChecker)
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        CheckToolName,
		Description: "Report the current public status of one external service (GitHub, OpenAI, Slack, ...).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args CheckArgs) (*mcp.CallToolResult, any, error) {
		return checkStatus(ctx, checker, args)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        ListToolName,
		Description: "List the external services whose public status can be checked.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ ListArgs) (*mcp.CallToolResult, any, error) {
		return listServices(ctx, checker)
	})
	return nil
}

// checkStatus reports the status of the named service. Each snapshot's health
// is also returned as structured output for callers that act on the result
// rather than display it.
func checkStatus(ctx context.Context, checker *Checker, args CheckArgs) (*mcp.CallToolResult, any, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("status: %w", err)
	}
	service := strings.TrimSpace(args.Service)
	if service == "" {
		return nil, nil, errors.New("status: check requires a service")
	}
	snapshots, err := checker.Check(ctx, service)
	if err != nil {
		return nil, nil, err
	}
	health := make(map[string]string, len(snapshots))
	for _, snapshot := range snapshots {
		health[snapshot.Provider] = string(snapshot.Health)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: formatSnapshots(snapshots)}},
		StructuredContent: health,
	}, nil, nil
}

// listServices lists the checkable services.
func listServices(ctx context.Context, checker *Checker) (*mcp.CallToolResult, any, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("status: %w", err)
	}
	names := checker.Names()
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: "Supported services: " + strings.Join(names, ", ")}},
		StructuredContent: names,
	}, nil, nil
}

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
