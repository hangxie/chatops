package status

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

func Test_Tool_invoke(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{
		name: "github",
		snap: Snapshot{Health: HealthDegraded, Summary: "Degraded Performance", Incidents: []Incident{{Name: "API errors", Status: "monitoring", URL: "https://example.test/incident"}}},
	}})
	require.NoError(t, err)
	tl, err := Open(context.Background(), checker)
	require.NoError(t, err)

	testCases := map[string]struct {
		call        tool.Call
		text        string
		details     map[string]string
		errIs       error
		errContains string
	}{
		"check": {
			call:    tool.Call{Action: "check", Target: "github"},
			text:    "[DEGRADED] GitHub — Degraded Performance\n  API errors (monitoring)\n  https://example.test/incident",
			details: map[string]string{"github": "degraded"},
		},
		"list":           {call: tool.Call{Action: "list"}, text: "Supported services: github"},
		"missing-target": {call: tool.Call{Action: "check"}, errContains: "target is required"},
		"parameters":     {call: tool.Call{Action: "check", Target: "github", Parameters: map[string]string{"x": "y"}}, errContains: "parameters"},
		"unknown-target": {call: tool.Call{Action: "check", Target: "missing"}, errIs: ErrUnknownProvider},
		"list-target":    {call: tool.Call{Action: "list", Target: "github"}, errContains: "no target"},
		"unknown-action": {call: tool.Call{Action: "remove"}, errIs: tool.ErrUnknownAction},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result, err := tl.Invoke(context.Background(), tc.call)
			if tc.errIs != nil || tc.errContains != "" {
				if tc.errIs != nil {
					require.ErrorIs(t, err, tc.errIs)
				}
				if tc.errContains != "" {
					require.ErrorContains(t, err, tc.errContains)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.text, result.Text)
			require.Equal(t, tc.details, result.Details)
		})
	}
}

func Test_Opener_validates_URL(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)
	opener := NewOpener(checker)

	testCases := map[string]struct {
		rawURL string
		err    bool
	}{
		"bare":  {rawURL: "status://"},
		"host":  {rawURL: "status://github", err: true},
		"query": {rawURL: "status://?timeout=1", err: true},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			require.NoError(t, err)
			tl, err := opener(context.Background(), u, nil)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NoError(t, tl.Close())
		})
	}
}

func Test_Tool_context_and_close(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)
	tl, err := Open(context.Background(), checker)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = tl.Invoke(ctx, tool.Call{Action: "list"})
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, tl.Close())
	require.NoError(t, tl.Close())
}

func Test_Open_rejects_nil_checker(t *testing.T) {
	_, err := Open(context.Background(), nil)
	require.True(t, errors.Is(err, ErrNilChecker))
}

func Test_Open_honors_cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, &Checker{})
	require.ErrorIs(t, err, context.Canceled)
}

func Test_Opener_uses_default_catalog(t *testing.T) {
	u, err := url.Parse("status://")
	require.NoError(t, err)
	tl, err := Opener(context.Background(), u, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tl.Close()) }()
	result, err := tl.Invoke(context.Background(), tool.Call{Action: "list"})
	require.NoError(t, err)
	require.Contains(t, result.Text, "github")
}

func Test_healthLabel_and_displayName(t *testing.T) {
	labels := map[Health]string{
		HealthOperational: "OK", HealthMaintenance: "MAINTENANCE", HealthDegraded: "DEGRADED",
		HealthPartialOutage: "PARTIAL OUTAGE", HealthMajorOutage: "MAJOR OUTAGE", HealthUnknown: "UNKNOWN",
	}
	for health, expected := range labels {
		require.Equal(t, expected, healthLabel(health))
	}
	require.Equal(t, "custom", displayName("custom"))
}
