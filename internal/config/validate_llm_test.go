package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validLLM() LLM {
	return LLM{
		Models:        []Model{{BaseURL: "http://localhost:8080/v1", Model: "m", Name: "m"}},
		Schedule:      ScheduleOrdered,
		MaxModelRetry: 1,
	}
}

func Test_validateLLM(t *testing.T) {
	backup := Model{BaseURL: "https://backup.example.internal/v1", Model: "b", Name: "b"}
	tests := map[string]struct {
		mutate func(*LLM)
		errs   []string
	}{
		"valid":             {mutate: func(*LLM) {}},
		"no-models":         {mutate: func(l *LLM) { l.Models = nil }, errs: []string{"llm.models: at least one model is required"}},
		"missing-base-url":  {mutate: func(l *LLM) { l.Models[0].BaseURL = "" }, errs: []string{"llm.models[0].base_url: required"}},
		"relative-base-url": {mutate: func(l *LLM) { l.Models[0].BaseURL = "localhost/v1" }, errs: []string{`llm.models[0].base_url: must be an absolute http or https URL, got "localhost/v1"`}},
		"ftp-base-url":      {mutate: func(l *LLM) { l.Models[0].BaseURL = "ftp://host/v1" }, errs: []string{`llm.models[0].base_url: must be an absolute http or https URL, got "ftp://host/v1"`}},
		"missing-model":     {mutate: func(l *LLM) { l.Models[0].Model = "" }, errs: []string{"llm.models[0].model: required"}},
		"missing-name":      {mutate: func(l *LLM) { l.Models[0].Name = "" }, errs: []string{"llm.models[0].name: required"}},
		"missing-names": {mutate: func(l *LLM) {
			l.Models = []Model{{BaseURL: "https://a/v1", Model: "a"}, {BaseURL: "https://b/v1", Model: "b"}}
			l.MaxModelRetry = 2
		}, errs: []string{"llm.models[0].name: required", "llm.models[1].name: required"}},
		"bad-api-key-env":  {mutate: func(l *LLM) { l.Models[0].APIKeyEnv = "1KEY" }, errs: []string{`llm.models[0].api_key_env: invalid environment variable name "1KEY"`}},
		"good-api-key-env": {mutate: func(l *LLM) { l.Models[0].APIKeyEnv = "LLM_API_KEY" }},
		"api-key-plaintext": {mutate: func(l *LLM) {
			l.Models[0] = Model{BaseURL: "http://llm.example.internal/v1", Model: "m", Name: "m", APIKeyEnv: "LLM_API_KEY"}
		}, errs: []string{`llm.models[0].api_key_env: requires an https base_url unless the host is loopback, got "http://llm.example.internal/v1"`}},
		"api-key-https": {mutate: func(l *LLM) {
			l.Models[0] = Model{BaseURL: "https://llm.example.internal/v1", Model: "m", Name: "m", APIKeyEnv: "LLM_API_KEY"}
		}},
		"retry-codes":      {mutate: func(l *LLM) { l.Models[0].RetryHTTPCodes = []string{"401", "403", "4xx", "5xx", "599"} }},
		"retry-codes-none": {mutate: func(l *LLM) { l.Models[0].RetryHTTPCodes = []string{} }},
		"retry-codes-bad": {mutate: func(l *LLM) { l.Models[0].RetryHTTPCodes = []string{"401", "abc", "399", "600", "3xx", ""} }, errs: []string{
			`llm.models[0].retry_http_codes: entry 1 must be a status from 400 to 599, 4xx, or 5xx; got "abc"`,
			`llm.models[0].retry_http_codes: entry 2 must be a status from 400 to 599, 4xx, or 5xx; got "399"`,
			`llm.models[0].retry_http_codes: entry 3 must be a status from 400 to 599, 4xx, or 5xx; got "600"`,
			`llm.models[0].retry_http_codes: entry 4 must be a status from 400 to 599, 4xx, or 5xx; got "3xx"`,
			`llm.models[0].retry_http_codes: entry 5 must be a status from 400 to 599, 4xx, or 5xx; got ""`,
		}},
		"timeout":          {mutate: func(l *LLM) { l.Models[0].Timeout = 30 * time.Second }},
		"negative-timeout": {mutate: func(l *LLM) { l.Models[0].Timeout = -time.Second }, errs: []string{"llm.models[0].timeout: must not be negative"}},
		"sampling-valid": {mutate: func(l *LLM) {
			l.Models[0].Sampling = Sampling{
				Temperature: pval(0.0), TopP: pval(1.0), TopK: pval(0), MinP: pval(0.0),
				MaxTokens: pval(256), FrequencyPenalty: pval(-2.0), PresencePenalty: pval(2.0),
				RepetitionPenalty: pval(1.0), Seed: pval(-5), Stop: []string{"END"},
			}
		}},
		"sampling-temperature-high": {mutate: func(l *LLM) { l.Models[0].Sampling.Temperature = pval(2.5) }, errs: []string{"llm.models[0].sampling.temperature: must be between 0 and 2, got 2.5"}},
		"sampling-top-p-low":        {mutate: func(l *LLM) { l.Models[0].Sampling.TopP = pval(-0.1) }, errs: []string{"llm.models[0].sampling.top_p: must be between 0 and 1, got -0.1"}},
		"sampling-min-p-high":       {mutate: func(l *LLM) { l.Models[0].Sampling.MinP = pval(1.5) }, errs: []string{"llm.models[0].sampling.min_p: must be between 0 and 1, got 1.5"}},
		"sampling-freq-penalty":     {mutate: func(l *LLM) { l.Models[0].Sampling.FrequencyPenalty = pval(3.0) }, errs: []string{"llm.models[0].sampling.frequency_penalty: must be between -2 and 2, got 3"}},
		"sampling-pres-penalty":     {mutate: func(l *LLM) { l.Models[0].Sampling.PresencePenalty = pval(-3.0) }, errs: []string{"llm.models[0].sampling.presence_penalty: must be between -2 and 2, got -3"}},
		"sampling-rep-penalty":      {mutate: func(l *LLM) { l.Models[0].Sampling.RepetitionPenalty = pval(0.0) }, errs: []string{"llm.models[0].sampling.repetition_penalty: must be positive"}},
		"sampling-top-k-negative":   {mutate: func(l *LLM) { l.Models[0].Sampling.TopK = pval(-1) }, errs: []string{"llm.models[0].sampling.top_k: must not be negative"}},
		"sampling-max-tokens-zero":  {mutate: func(l *LLM) { l.Models[0].Sampling.MaxTokens = pval(0) }, errs: []string{"llm.models[0].sampling.max_tokens: must be positive"}},
		"sampling-empty-stop":       {mutate: func(l *LLM) { l.Models[0].Sampling.Stop = []string{"ok", ""} }, errs: []string{"llm.models[0].sampling.stop: entry 1 is empty"}},
		"second-model-invalid": {mutate: func(l *LLM) {
			l.Models = append(l.Models, Model{BaseURL: "https://h/v1", Name: "x"})
			l.MaxModelRetry = 2
		}, errs: []string{"llm.models[1].model: required"}},
		"same-model-distinct-names": {mutate: func(l *LLM) {
			l.Models = append(l.Models, Model{BaseURL: "https://replica/v1", Model: "m", Name: "m-replica"})
			l.MaxModelRetry = 2
		}},
		"duplicate-names": {mutate: func(l *LLM) {
			l.Models = append(l.Models, backup, Model{BaseURL: "https://replica/v1", Model: "m", Name: "m"})
			l.MaxModelRetry = 3
		}, errs: []string{`llm.models[2].name: "m" is already used by llm.models[0]; give each model a unique name`}},
		"round-robin": {mutate: func(l *LLM) { l.Schedule = ScheduleRoundRobin }},
		"sticky":      {mutate: func(l *LLM) { l.Schedule = ScheduleSticky }},
		"bad-schedule": {mutate: func(l *LLM) { l.Schedule = "random" }, errs: []string{
			`llm.schedule: must be one of "ordered", "round_robin", "sticky"; got "random"`,
		}},
		"retry-every-model": {mutate: func(l *LLM) { l.Models = append(l.Models, backup); l.MaxModelRetry = 2 }},
		"retry-fewer":       {mutate: func(l *LLM) { l.Models = append(l.Models, backup); l.MaxModelRetry = 1 }},
		"retry-zero":        {mutate: func(l *LLM) { l.MaxModelRetry = 0 }, errs: []string{"llm.max_model_retry: must be positive"}},
		"retry-more-than-models": {mutate: func(l *LLM) { l.Models = append(l.Models, backup); l.MaxModelRetry = 3 }, errs: []string{
			"llm.max_model_retry: must not exceed the 2 configured models, got 3",
		}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg.LLM)
			err := cfg.Validate()
			if len(tc.errs) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, tc.errs, splitErrors(err))
		})
	}
}
