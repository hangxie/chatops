package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Model_Streaming(t *testing.T) {
	tests := map[string]struct {
		stream *bool
		want   bool
	}{
		"unset": {stream: nil, want: true},
		"off":   {stream: pval(false), want: false},
		"on":    {stream: pval(true), want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, Model{Stream: tc.stream}.Streaming())
		})
	}
}

func Test_LLM_withDefaults(t *testing.T) {
	two := func() []Model { return []Model{{Model: "a"}, {Model: "b", Name: "backup"}} }
	named := []Model{{Model: "a", Name: "a"}, {Model: "b", Name: "backup"}}
	tests := map[string]struct {
		in   LLM
		want LLM
	}{
		"empty":         {in: LLM{}, want: LLM{Schedule: ScheduleOrdered}},
		"every-model":   {in: LLM{Models: two()}, want: LLM{Models: named, Schedule: ScheduleOrdered, MaxModelRetry: 2}},
		"keeps-set":     {in: LLM{Models: two(), Schedule: ScheduleSticky, MaxModelRetry: 1}, want: LLM{Models: named, Schedule: ScheduleSticky, MaxModelRetry: 1}},
		"keeps-invalid": {in: LLM{Models: two(), MaxModelRetry: -1}, want: LLM{Models: named, Schedule: ScheduleOrdered, MaxModelRetry: -1}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.in.withDefaults())
		})
	}
}

func Test_LLM_withDefaults_copies_models(t *testing.T) {
	in := LLM{Models: []Model{{Model: "a"}}}
	_ = in.withDefaults()
	require.Empty(t, in.Models[0].Name)
}

func Test_Model_RetriesStatus(t *testing.T) {
	tests := map[string]struct {
		codes []string
		retry []int
		stay  []int
	}{
		"default":   {codes: nil, retry: []int{429, 500, 503, 599}, stay: []int{400, 401, 403, 404, 428}},
		"none":      {codes: []string{}, stay: []int{401, 429, 500, 503}},
		"codes":     {codes: []string{"401", "403", "503"}, retry: []int{401, 403, 503}, stay: []int{400, 429, 500}},
		"class-4xx": {codes: []string{"4xx"}, retry: []int{400, 401, 499}, stay: []int{500, 503}},
		"mixed":     {codes: []string{"401", "5xx"}, retry: []int{401, 500, 599}, stay: []int{400, 403, 429}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			model := Model{RetryHTTPCodes: tc.codes}
			for _, code := range tc.retry {
				require.True(t, model.RetriesStatus(code), code)
			}
			for _, code := range tc.stay {
				require.False(t, model.RetriesStatus(code), code)
			}
		})
	}
}
