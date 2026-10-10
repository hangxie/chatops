package status

import (
	"context"
	"net/http"
)

// slackStatus reads Slack's current-status endpoint.
func slackStatus(endpoint string) checkFunc {
	return func(ctx context.Context, client *http.Client) (Snapshot, error) {
		var response struct {
			Status    string `json:"status"`
			Incidents []struct {
				Title  string `json:"title"`
				Type   string `json:"type"`
				Status string `json:"status"`
				URL    string `json:"url"`
			} `json:"active_incidents"`
		}
		if err := fetchJSON(ctx, client, endpoint, &response); err != nil {
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
		if response.Status != "ok" && len(incidents) == 0 {
			health = HealthUnknown
		}
		return Snapshot{Health: health, Summary: activeSummary("", len(incidents)), Incidents: incidents}, nil
	}
}
