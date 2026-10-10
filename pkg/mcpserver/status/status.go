// Package status reports the public status of common external services from their vendors' status pages.
package status

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var healthLabels = map[Health]string{
	HealthOperational:   "OK",
	HealthMaintenance:   "MAINTENANCE",
	HealthDegraded:      "DEGRADED",
	HealthPartialOutage: "PARTIAL OUTAGE",
	HealthMajorOutage:   "MAJOR OUTAGE",
	HealthUnknown:       "UNKNOWN",
}

// Input is the service_status tool's arguments.
type Input struct {
	Service string `json:"service"`
}

// Output is the structured result of the service_status tool.
type Output struct {
	Services []Snapshot `json:"services"`
}

// Register adds the service_status tool, checking the services cfg lists, to server.
func Register(server *mcp.Server, cfg Config) error {
	c, err := newChecker(cfg)
	if err != nil {
		return err
	}
	register(server, c)
	return nil
}

func register(server *mcp.Server, c *checker) {
	enum := []any{allServices}
	choices := make([]string, 0, len(c.services))
	for _, s := range c.services {
		enum = append(enum, s.name)
		choice := s.name
		if len(s.covers) != 0 {
			choice += " (" + strings.Join(s.covers, ", ") + ")"
		}
		choices = append(choices, choice)
	}
	openWorld := true
	mcp.AddTool(server, &mcp.Tool{
		Name:        "service_status",
		Description: "Report the current public status of an external service from its vendor's status page, including active incidents. Use service \"all\" to check every service. Only the listed services are covered: for any other service, do not call this tool; say it is not covered.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"service": {Type: "string", Description: "The service to check, or \"all\" for every one: " + strings.Join(choices, "; ") + ".", Enum: enum},
			},
			Required:             []string{"service"},
			AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		},
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  &openWorld,
		},
	}, c.handle)
}

func (c *checker) handle(ctx context.Context, _ *mcp.CallToolRequest, in Input) (*mcp.CallToolResult, Output, error) {
	snapshots, err := c.check(ctx, in.Service)
	if err != nil {
		return nil, Output{}, err
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: format(snapshots)}}}
	return result, Output{Services: snapshots}, nil
}

func format(snapshots []Snapshot) string {
	var lines []string
	for _, s := range snapshots {
		lines = append(lines, fmt.Sprintf("[%s] %s - %s", healthLabels[s.Health], s.Service, s.Summary))
		for _, incident := range s.Incidents {
			line := "  " + incident.Name
			if incident.Status != "" {
				line += " (" + incident.Status + ")"
			}
			if incident.URL != "" {
				line += " " + incident.URL
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
