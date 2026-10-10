package status

import (
	"context"
	"net/http"
)

// statuspage reads an Atlassian Statuspage summary endpoint.
func statuspage(endpoint string) checkFunc {
	return func(ctx context.Context, client *http.Client) (Snapshot, error) {
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
		if err := fetchJSON(ctx, client, endpoint, &response); err != nil {
			return Snapshot{}, err
		}
		health := map[string]Health{
			"none": HealthOperational, "maintenance": HealthMaintenance, "minor": HealthDegraded,
			"major": HealthPartialOutage, "critical": HealthMajorOutage,
		}[response.Status.Indicator]
		if health == "" {
			health = HealthUnknown
		}
		incidents := make([]Incident, 0, len(response.Incidents))
		for _, item := range response.Incidents {
			incidents = append(incidents, Incident{Name: item.Name, Status: item.Status, URL: item.Shortlink})
		}
		return Snapshot{Health: health, Summary: response.Status.Description, Incidents: incidents}, nil
	}
}
