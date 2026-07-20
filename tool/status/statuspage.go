package status

import (
	"context"
	"net/http"
)

type statuspageProvider struct {
	name     string
	aliases  []string
	endpoint string
	client   *http.Client
}

// NewStatuspageProvider creates a provider for an Atlassian Statuspage summary endpoint.
func NewStatuspageProvider(name string, aliases []string, endpoint string, client *http.Client) Provider {
	return &statuspageProvider{name: name, aliases: aliases, endpoint: endpoint, client: client}
}

func (p *statuspageProvider) Name() string      { return p.name }
func (p *statuspageProvider) Aliases() []string { return append([]string(nil), p.aliases...) }

func (p *statuspageProvider) Check(ctx context.Context) (Snapshot, error) {
	var response struct {
		Status struct {
			Indicator   string `json:"indicator"`
			Description string `json:"description"`
		} `json:"status"`
		Incidents []struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			Shortlink string `json:"shortlink"`
		} `json:"incidents"`
	}
	if err := fetchJSON(ctx, p.client, p.endpoint, &response); err != nil {
		return Snapshot{}, err
	}
	health := map[string]Health{
		"none": HealthOperational, "minor": HealthDegraded, "major": HealthPartialOutage, "critical": HealthMajorOutage,
	}[response.Status.Indicator]
	if health == "" {
		health = HealthUnknown
	}
	incidents := make([]Incident, 0, len(response.Incidents))
	for _, incident := range response.Incidents {
		incidents = append(incidents, Incident{Name: incident.Name, Status: incident.Status, URL: incident.Shortlink})
	}
	return Snapshot{Health: health, Summary: response.Status.Description, Incidents: incidents}, nil
}
