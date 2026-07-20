package status

import (
	"context"
	"net/http"
)

type slackProvider struct {
	endpoint string
	client   *http.Client
}

// NewSlackProvider creates a provider for Slack's current-status endpoint.
func NewSlackProvider(endpoint string, client *http.Client) Provider {
	return &slackProvider{endpoint: endpoint, client: client}
}

func (p *slackProvider) Name() string      { return "slack" }
func (p *slackProvider) Aliases() []string { return nil }

func (p *slackProvider) Check(ctx context.Context) (Snapshot, error) {
	var response struct {
		Status    string `json:"status"`
		Incidents []struct {
			Title  string `json:"title"`
			Type   string `json:"type"`
			Status string `json:"status"`
			URL    string `json:"url"`
		} `json:"active_incidents"`
	}
	if err := fetchJSON(ctx, p.client, p.endpoint, &response); err != nil {
		return Snapshot{}, err
	}
	health := HealthOperational
	incidents := make([]Incident, 0, len(response.Incidents))
	for _, item := range response.Incidents {
		incidentHealth := HealthDegraded
		if item.Type == "outage" {
			incidentHealth = HealthMajorOutage
		}
		health = worstHealth(health, incidentHealth)
		incidents = append(incidents, Incident{Name: item.Title, Status: item.Status, URL: item.URL})
	}
	if response.Status != "ok" && len(response.Incidents) == 0 {
		health = HealthUnknown
	}
	summary := "All Systems Operational"
	if len(incidents) != 0 {
		summary = "Active incident"
		if len(incidents) > 1 {
			summary = "Active incidents"
		}
	}
	return Snapshot{Health: health, Summary: summary, Incidents: incidents}, nil
}
