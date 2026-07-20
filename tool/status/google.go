package status

import (
	"context"
	"net/http"
	"strings"
)

const (
	vertexGeminiProductID    = "Z0FZJAMvEB4j3NbCJs6B"
	workspaceGeminiProductID = "npdyhgECDJ6tB66MxXyo"
)

type googleProvider struct {
	cloudEndpoint     string
	workspaceEndpoint string
	client            *http.Client
}

// NewGoogleProvider creates an aggregate provider for Vertex and Workspace Gemini.
func NewGoogleProvider(cloudEndpoint, workspaceEndpoint string, client *http.Client) Provider {
	return &googleProvider{cloudEndpoint: cloudEndpoint, workspaceEndpoint: workspaceEndpoint, client: client}
}

func (p *googleProvider) Name() string { return "gemini" }
func (p *googleProvider) Aliases() []string {
	return []string{"google", "google-gemini", "gemini-api", "vertex-gemini", "gemini-workspace"}
}

type googleIncident struct {
	ID          string `json:"id"`
	End         string `json:"end"`
	Description string `json:"external_desc"`
	Products    []struct {
		ID string `json:"id"`
	} `json:"affected_products"`
	MostRecent struct {
		Status string `json:"status"`
	} `json:"most_recent_update"`
}

func (p *googleProvider) Check(ctx context.Context) (Snapshot, error) {
	var cloud, workspace []googleIncident
	if err := fetchJSON(ctx, p.client, p.cloudEndpoint, &cloud); err != nil {
		return Snapshot{}, err
	}
	if err := fetchJSON(ctx, p.client, p.workspaceEndpoint, &workspace); err != nil {
		return Snapshot{}, err
	}
	active := append(filterGoogleIncidents(cloud, vertexGeminiProductID), filterGoogleIncidents(workspace, workspaceGeminiProductID)...)
	health := HealthOperational
	incidents := make([]Incident, 0, len(active))
	for _, item := range active {
		incidentHealth := googleHealth(item.MostRecent.Status)
		health = worstHealth(health, incidentHealth)
		incidents = append(incidents, Incident{Name: firstLine(item.Description), Status: strings.ToLower(item.MostRecent.Status)})
	}
	summary := "All Systems Operational"
	if len(incidents) != 0 {
		summary = "Active Gemini incident"
		if len(incidents) > 1 {
			summary = "Active Gemini incidents"
		}
	}
	return Snapshot{Health: health, Summary: summary, Incidents: incidents}, nil
}

func filterGoogleIncidents(incidents []googleIncident, productID string) []googleIncident {
	result := make([]googleIncident, 0)
	for _, incident := range incidents {
		if incident.End != "" {
			continue
		}
		for _, product := range incident.Products {
			if product.ID == productID {
				result = append(result, incident)
				break
			}
		}
	}
	return result
}

func googleHealth(status string) Health {
	switch strings.ToUpper(status) {
	case "SERVICE_OUTAGE":
		return HealthMajorOutage
	case "SERVICE_DISRUPTION":
		return HealthDegraded
	case "AVAILABLE":
		return HealthOperational
	default:
		return HealthDegraded
	}
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	runes := []rune(line)
	if len(runes) > 200 {
		return string(runes[:197]) + "..."
	}
	return line
}
