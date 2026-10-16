package status

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxErrorDetail caps how much of a failing response body the summary quotes.
const maxErrorDetail = 200

var healthyStatuses = map[string]bool{"ok": true, "healthy": true, "up": true, "pass": true}

// healthEndpoint probes a self-hosted health endpoint, where no answer means the service is down.
func healthEndpoint(endpoint string) checkFunc {
	return func(ctx context.Context, client *http.Client) (Snapshot, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return Snapshot{}, fmt.Errorf("create request: %w", err)
		}
		request.Header.Set("User-Agent", "chatops-mcp-status")
		response, err := client.Do(request)
		if err != nil {
			return Snapshot{Health: HealthMajorOutage, Summary: "Unreachable: " + err.Error()}, nil
		}
		// A close error after a full read changes nothing about the service's health.
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		if err != nil {
			return Snapshot{Health: HealthMajorOutage, Summary: "Unreadable response: " + err.Error()}, nil
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return Snapshot{Health: HealthMajorOutage, Summary: failureSummary(response.Status, body)}, nil
		}
		return healthReport(body), nil
	}
}

// healthReport reads an optional JSON status and component list loosely, so one malformed field cannot hide the rest.
func healthReport(body []byte) Snapshot {
	snapshot := Snapshot{Health: HealthOperational, Summary: "Healthy"}
	var report map[string]any
	if json.Unmarshal(body, &report) != nil {
		return snapshot
	}
	if !healthyStatus(report["status"]) {
		snapshot = Snapshot{Health: HealthPartialOutage, Summary: "Reports status " + statusText(report["status"])}
	}
	components, _ := report["components"].([]any)
	for _, item := range components {
		component, ok := item.(map[string]any)
		if !ok || (component["status"] != nil && healthyStatus(component["status"])) {
			continue
		}
		snapshot.Health = HealthPartialOutage
		snapshot.Incidents = append(snapshot.Incidents, Incident{Name: plainText(component["name"]), Status: plainText(component["status"])})
	}
	if snapshot.Summary == "Healthy" && len(snapshot.Incidents) > 0 {
		snapshot.Summary = fmt.Sprintf("%d unhealthy component", len(snapshot.Incidents))
		if len(snapshot.Incidents) > 1 {
			snapshot.Summary += "s"
		}
	}
	return snapshot
}

// healthyStatus accepts a missing status, true, or a healthy word; any other value present is a reported problem.
func healthyStatus(status any) bool {
	switch v := status.(type) {
	case nil:
		return true
	case bool:
		return v
	case string:
		return healthyStatuses[strings.ToLower(v)]
	}
	return false
}

func statusText(status any) string {
	if s, ok := status.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return plainText(status)
}

// plainText renders a decoded JSON value: strings as is, nothing as empty, anything else as JSON.
func plainText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	text, _ := json.Marshal(value)
	return string(text)
}

func failureSummary(status string, body []byte) string {
	var lines []string
	for line := range strings.Lines(string(body)) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	detail := strings.Join(lines, "; ")
	if detail == "" {
		return "HTTP " + status
	}
	if len(detail) > maxErrorDetail {
		detail = strings.ToValidUTF8(detail[:maxErrorDetail], "") + "..."
	}
	return "HTTP " + status + ": " + detail
}
