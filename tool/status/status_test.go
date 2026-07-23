package status

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

// openCheck and openList build the single-intent tools against checker
// through their openers, using the bare URL each accepts.
func openCheck(t *testing.T, checker *Checker) tool.Tool {
	t.Helper()
	u, err := url.Parse(CheckScheme + "://")
	require.NoError(t, err)
	tl, err := NewCheckOpener(checker)(context.Background(), u, nil)
	require.NoError(t, err)
	return tl
}

func openList(t *testing.T, checker *Checker) tool.Tool {
	t.Helper()
	u, err := url.Parse(ListScheme + "://")
	require.NoError(t, err)
	tl, err := NewListOpener(checker)(context.Background(), u, nil)
	require.NoError(t, err)
	return tl
}

func Test_checkTool_invoke(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{
		name: "github",
		snap: Snapshot{Health: HealthDegraded, Summary: "Degraded Performance", Incidents: []Incident{{Name: "API errors", Status: "monitoring", URL: "https://example.test/incident"}}},
	}})
	require.NoError(t, err)
	tl := openCheck(t, checker)

	testCases := map[string]struct {
		call        tool.Call
		text        string
		details     map[string]string
		errIs       error
		errContains string
	}{
		"check": {
			call:    tool.Call{Arguments: map[string]string{serviceParam: "github"}},
			text:    "[DEGRADED] GitHub — Degraded Performance\n  API errors (monitoring)\n  https://example.test/incident",
			details: map[string]string{"github": "degraded"},
		},
		"missing-service": {call: tool.Call{}, errContains: "requires a service"},
		"blank-service":   {call: tool.Call{Arguments: map[string]string{serviceParam: "  "}}, errContains: "requires a service"},
		"unknown-service": {call: tool.Call{Arguments: map[string]string{serviceParam: "missing"}}, errIs: ErrUnknownProvider},
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

func Test_listTool_invoke(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)
	tl := openList(t, checker)

	// The list tool ignores any arguments and reports the catalog.
	result, err := tl.Invoke(context.Background(), tool.Call{Arguments: map[string]string{"ignored": "x"}})
	require.NoError(t, err)
	require.Equal(t, "Supported services: github", result.Text)
}

func Test_Opener_validates_URL(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)

	testCases := map[string]struct {
		opener tool.OpenerFunc
		scheme string
	}{
		"check": {opener: NewCheckOpener(checker), scheme: CheckScheme},
		"list":  {opener: NewListOpener(checker), scheme: ListScheme},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for suffix, wantErr := range map[string]bool{"://": false, "://github": true, "://?timeout=1": true} {
				u, err := url.Parse(tc.scheme + suffix)
				require.NoError(t, err)
				tl, err := tc.opener(context.Background(), u, nil)
				if wantErr {
					require.Error(t, err)
					continue
				}
				require.NoError(t, err)
				require.NoError(t, tl.Close())
			}
		})
	}
}

func Test_Tool_context_and_close(t *testing.T) {
	checker, err := NewChecker([]Provider{fakeProvider{name: "github"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tl := range []tool.Tool{openCheck(t, checker), openList(t, checker)} {
		_, err = tl.Invoke(ctx, tool.Call{})
		require.ErrorIs(t, err, context.Canceled)
		require.NoError(t, tl.Close())
		require.NoError(t, tl.Close())
	}
}

func Test_Opener_rejects_nil_checker(t *testing.T) {
	u, err := url.Parse(CheckScheme + "://")
	require.NoError(t, err)
	_, err = NewCheckOpener(nil)(context.Background(), u, nil)
	require.ErrorIs(t, err, ErrNilChecker)
}

func Test_Opener_honors_cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u, err := url.Parse(ListScheme + "://")
	require.NoError(t, err)
	_, err = NewListOpener(&Checker{})(ctx, u, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func Test_Opener_uses_default_catalog(t *testing.T) {
	// Each default opener opens against its own scheme URL.
	for _, tc := range []struct {
		opener tool.OpenerFunc
		scheme string
	}{
		{CheckOpener, CheckScheme},
		{ListOpener, ListScheme},
	} {
		u, err := url.Parse(tc.scheme + "://")
		require.NoError(t, err)
		tl, err := tc.opener(context.Background(), u, nil)
		require.NoError(t, err)
		require.NoError(t, tl.Close())
	}

	// The default list tool reports the built-in catalog.
	u, err := url.Parse(ListScheme + "://")
	require.NoError(t, err)
	tl, err := ListOpener(context.Background(), u, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tl.Close()) }()
	result, err := tl.Invoke(context.Background(), tool.Call{})
	require.NoError(t, err)
	require.Contains(t, result.Text, "github")
}

func Test_Descriptors(t *testing.T) {
	require.NoError(t, CheckDescriptor.Validate())
	require.NoError(t, ListDescriptor.Validate())

	// The check tool declares a required "service" argument; list takes none.
	require.Len(t, CheckDescriptor.Parameters, 1)
	require.Equal(t, serviceParam, CheckDescriptor.Parameters[0].Name)
	require.True(t, CheckDescriptor.Parameters[0].Required)
	require.Empty(t, ListDescriptor.Parameters)
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
