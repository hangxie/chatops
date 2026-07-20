package status

import (
	"context"
	"net/http"
)

type statusIOProvider struct {
	name     string
	aliases  []string
	endpoint string
	client   *http.Client
}

// NewStatusIOProvider creates a provider for a Status.io public status endpoint.
func NewStatusIOProvider(name string, aliases []string, endpoint string, client *http.Client) Provider {
	return &statusIOProvider{name: name, aliases: aliases, endpoint: endpoint, client: client}
}

func (p *statusIOProvider) Name() string      { return p.name }
func (p *statusIOProvider) Aliases() []string { return append([]string(nil), p.aliases...) }

func (p *statusIOProvider) Check(ctx context.Context) (Snapshot, error) {
	var response struct {
		Result struct {
			Overall struct {
				Status string `json:"status"`
				Code   int    `json:"status_code"`
			} `json:"status_overall"`
			Incidents []struct {
				Name string `json:"name"`
			} `json:"incidents"`
		} `json:"result"`
	}
	if err := fetchJSON(ctx, p.client, p.endpoint, &response); err != nil {
		return Snapshot{}, err
	}
	health := map[int]Health{
		100: HealthOperational, 200: HealthMaintenance, 300: HealthDegraded,
		400: HealthPartialOutage, 500: HealthMajorOutage, 600: HealthDegraded,
	}[response.Result.Overall.Code]
	if health == "" {
		health = HealthUnknown
	}
	incidents := make([]Incident, 0, len(response.Result.Incidents))
	for _, item := range response.Result.Incidents {
		incidents = append(incidents, Incident{Name: item.Name})
	}
	return Snapshot{Health: health, Summary: response.Result.Overall.Status, Incidents: incidents}, nil
}
