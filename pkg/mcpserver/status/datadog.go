package status

import (
	"context"
	"net/http"
	"strings"
)

var datadogHealth = map[string]Health{
	"operational": HealthOperational, "maintenance": HealthMaintenance, "degraded": HealthDegraded,
	"partial_outage": HealthPartialOutage, "major_outage": HealthMajorOutage,
}

var healthLabel = map[Health]string{
	HealthMaintenance: "maintenance", HealthDegraded: "degraded", HealthPartialOutage: "partial outage",
	HealthMajorOutage: "major outage", HealthUnknown: "unknown",
}

type datadogComponent struct {
	Name       string             `json:"name"`
	Status     string             `json:"status"`
	Components []datadogComponent `json:"components"`
}

// datadog reads a Datadog Status Pages config.json, which the page's own JavaScript renders.
func datadog(endpoint string) checkFunc {
	return func(ctx context.Context, client *http.Client) (Snapshot, error) {
		var response struct {
			Components []datadogComponent `json:"components"`
			Incidents  []struct {
				Title         string `json:"title"`
				CurrentStatus string `json:"currentStatus"`
				Resolved      bool   `json:"resolved"`
			} `json:"incidents"`
		}
		if err := fetchJSON(ctx, client, endpoint, &response); err != nil {
			return Snapshot{}, err
		}
		incidents := []Incident{}
		for _, item := range response.Incidents {
			if !item.Resolved {
				incidents = append(incidents, Incident{Name: item.Title, Status: item.CurrentStatus})
			}
		}
		health, summary := datadogSummary(response.Components, len(incidents))
		return Snapshot{Health: health, Summary: summary, Incidents: incidents}, nil
	}
}

// datadogSummary takes the worst component health and names the affected components, worst first.
func datadogSummary(components []datadogComponent, incidents int) (Health, string) {
	affected := map[Health][]string{}
	health, reported := HealthOperational, 0
	var walk func([]datadogComponent)
	walk = func(items []datadogComponent) {
		for _, c := range items {
			walk(c.Components)
			// Groups usually omit their own status and leave it to their children.
			if len(c.Components) != 0 && c.Status == "" {
				continue
			}
			reported++
			h, ok := datadogHealth[c.Status]
			if !ok {
				h = HealthUnknown
			}
			health = worstHealth(health, h)
			if h != HealthOperational {
				affected[h] = append(affected[h], c.Name)
			}
		}
	}
	walk(components)
	if reported == 0 && incidents == 0 {
		return HealthUnknown, "No components reported"
	}
	if reported == 0 {
		return HealthUnknown, activeSummary("", incidents)
	}
	var parts []string
	for _, h := range []Health{HealthUnknown, HealthMajorOutage, HealthPartialOutage, HealthDegraded, HealthMaintenance} {
		if names := affected[h]; len(names) != 0 {
			parts = append(parts, healthLabel[h]+": "+strings.Join(names, ", "))
		}
	}
	if len(parts) == 0 {
		return health, activeSummary("", incidents)
	}
	summary := strings.Join(parts, "; ")
	return health, strings.ToUpper(summary[:1]) + summary[1:]
}
