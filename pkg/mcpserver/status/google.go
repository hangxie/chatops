package status

import (
	"context"
	"net/http"
	"slices"
	"strings"
)

type googleIncident struct {
	End         string `json:"end"`
	Description string `json:"external_desc"`
	Products    []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"affected_products"`
	MostRecent struct {
		Status string `json:"status"`
	} `json:"most_recent_update"`
}

// google combines the open incidents of Google status dashboard feeds, such as Cloud or Workspace.
func google(feeds []Feed, display string) checkFunc {
	return func(ctx context.Context, client *http.Client) (Snapshot, error) {
		var active []googleIncident
		for _, feed := range feeds {
			var incidents []googleIncident
			if err := fetchJSON(ctx, client, feed.URL, &incidents); err != nil {
				return Snapshot{}, err
			}
			active = append(active, openIncidents(incidents, feed.Products)...)
		}
		return googleSnapshot(active, display+" "), nil
	}
}

// googleSnapshot names each incident after its affected products, so "Gmail: ..." says what is broken.
func googleSnapshot(active []googleIncident, subject string) Snapshot {
	health := HealthOperational
	incidents := make([]Incident, 0, len(active))
	for _, item := range active {
		health = worstHealth(health, googleHealth(item.MostRecent.Status))
		titles := make([]string, 0, len(item.Products))
		for _, product := range item.Products {
			titles = append(titles, product.Title)
		}
		incidents = append(incidents, Incident{
			Name:   strings.Join(titles, ", ") + ": " + firstLine(item.Description),
			Status: strings.ToLower(item.MostRecent.Status),
		})
	}
	return Snapshot{Health: health, Summary: activeSummary(subject, len(incidents)), Incidents: incidents}
}

// openIncidents keeps unfinished incidents affecting any of products, or every one when products is empty.
func openIncidents(incidents []googleIncident, products []string) []googleIncident {
	var open []googleIncident
	for _, incident := range incidents {
		if incident.End != "" {
			continue
		}
		for _, product := range incident.Products {
			if len(products) == 0 || slices.Contains(products, product.ID) {
				open = append(open, incident)
				break
			}
		}
	}
	return open
}

func googleHealth(status string) Health {
	switch strings.ToUpper(status) {
	case "SERVICE_OUTAGE":
		return HealthMajorOutage
	case "AVAILABLE":
		return HealthOperational
	}
	return HealthDegraded
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	runes := []rune(line)
	if len(runes) > 200 {
		return string(runes[:197]) + "..."
	}
	return line
}
