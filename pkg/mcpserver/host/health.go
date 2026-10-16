package host

import "strings"

// Outcome is how one check ended.
type Outcome string

const (
	// OutcomePass means the host answered as expected.
	OutcomePass Outcome = "pass"
	// OutcomeFail means the probe ran and the host did not answer as expected.
	OutcomeFail Outcome = "fail"
	// OutcomeError means the checker could not run the probe, which says nothing about the host.
	OutcomeError Outcome = "error"
)

// Health is what the checks observed about a host from where chatops-mcp runs.
type Health string

const (
	HealthUp          Health = "up"
	HealthDegraded    Health = "degraded"
	HealthUnreachable Health = "unreachable"
	HealthUnknown     Health = "unknown"
)

// Check is one check's result.
type Check struct {
	Name    string  `json:"name"`
	Outcome Outcome `json:"outcome" jsonschema:"pass, fail, or error"`
	Detail  string  `json:"detail"`
	// Responded marks a failure where something at the address still answered, such as a refused connection.
	Responded bool `json:"responded,omitempty"`
}

// classify derives a host's health and summary from its enabled probes, claiming no more than they observed.
func classify(probes []Check) (Health, string) {
	var passed, failed, errored, answered []string
	for _, probe := range probes {
		switch probe.Outcome {
		case OutcomePass:
			passed = append(passed, probe.Name)
		case OutcomeFail:
			failed = append(failed, probe.Name)
			if probe.Responded {
				answered = append(answered, probe.Name)
			}
		default:
			errored = append(errored, probe.Name)
		}
	}
	switch {
	case len(probes) == 0:
		return HealthUnknown, "no check is enabled"
	case len(passed) == len(probes):
		return HealthUp, "every check passed"
	case len(passed) != 0:
		return HealthDegraded, phrases(passed, failed, errored)
	case len(answered) != 0:
		return HealthDegraded, "no check passed, but the host answered " + strings.Join(answered, ", ")
	case len(errored) == 0:
		return HealthUnreachable, "no probe answered from chatops-mcp"
	case len(failed) == 0:
		return HealthUnknown, "no check could run: " + strings.Join(errored, ", ")
	}
	return HealthUnknown, "no check passed, and " + strings.Join(errored, ", ") + " could not run"
}

func phrases(passed, failed, errored []string) string {
	var parts []string
	for _, group := range []struct {
		names []string
		verb  string
	}{{passed, "passed"}, {failed, "failed"}, {errored, "could not run"}} {
		if len(group.names) != 0 {
			parts = append(parts, strings.Join(group.names, ", ")+" "+group.verb)
		}
	}
	return strings.Join(parts, "; ")
}
