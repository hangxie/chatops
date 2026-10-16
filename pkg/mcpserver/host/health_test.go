package host

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_classify(t *testing.T) {
	pass := func(name string) Check { return Check{Name: name, Outcome: OutcomePass} }
	fail := func(name string) Check { return Check{Name: name, Outcome: OutcomeFail} }
	refused := func(name string) Check { return Check{Name: name, Outcome: OutcomeFail, Responded: true} }
	broken := func(name string) Check { return Check{Name: name, Outcome: OutcomeError} }
	tests := map[string]struct {
		probes  []Check
		health  Health
		summary string
	}{
		"all-pass":              {probes: []Check{pass("ping"), pass("ssh")}, health: HealthUp, summary: "every check passed"},
		"one-check-pass":        {probes: []Check{pass("ssh")}, health: HealthUp, summary: "every check passed"},
		"pass-and-refused":      {probes: []Check{pass("ping"), refused("ssh")}, health: HealthDegraded, summary: "ping passed; ssh failed"},
		"timeout-and-pass":      {probes: []Check{fail("ping"), pass("ssh")}, health: HealthDegraded, summary: "ssh passed; ping failed"},
		"error-and-pass":        {probes: []Check{broken("ping"), pass("ssh")}, health: HealthDegraded, summary: "ssh passed; ping could not run"},
		"all-fail":              {probes: []Check{fail("ping"), fail("ssh")}, health: HealthUnreachable, summary: "no probe answered from chatops-mcp"},
		"one-check-fail":        {probes: []Check{fail("ping")}, health: HealthUnreachable, summary: "no probe answered from chatops-mcp"},
		"fail-but-refused":      {probes: []Check{fail("ping"), refused("ssh")}, health: HealthDegraded, summary: "no check passed, but the host answered ssh"},
		"error-and-refused":     {probes: []Check{broken("ping"), refused("ssh")}, health: HealthDegraded, summary: "no check passed, but the host answered ssh"},
		"error-and-fail":        {probes: []Check{broken("ping"), fail("ssh")}, health: HealthUnknown, summary: "no check passed, and ping could not run"},
		"error-ssh-disabled":    {probes: []Check{broken("ping")}, health: HealthUnknown, summary: "no check could run: ping"},
		"all-error":             {probes: []Check{broken("ping"), broken("ssh")}, health: HealthUnknown, summary: "no check could run: ping, ssh"},
		"no-checks":             {health: HealthUnknown, summary: "no check is enabled"},
		"pass-fail-and-error":   {probes: []Check{pass("a"), fail("b"), broken("c")}, health: HealthDegraded, summary: "a passed; b failed; c could not run"},
		"unknown-outcome-error": {probes: []Check{{Name: "ping", Outcome: "weird"}}, health: HealthUnknown, summary: "no check could run: ping"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			health, summary := classify(tc.probes)
			require.Equal(t, tc.health, health)
			require.Equal(t, tc.summary, summary)
		})
	}
}
