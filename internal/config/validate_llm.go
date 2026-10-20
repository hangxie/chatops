package config

import (
	"fmt"
	"strconv"
)

func validateLLM(llm LLM, add addFunc) {
	if len(llm.Models) == 0 {
		add("llm.models", "at least one model is required")
	}
	firstUse := map[string]int{}
	for i, model := range llm.Models {
		prefix := fmt.Sprintf("llm.models[%d]", i)
		validateModel(prefix, model, add)
		if model.Name == "" {
			add(prefix+".name", "required")
		} else if first, ok := firstUse[model.Name]; ok {
			add(prefix+".name", "%q is already used by llm.models[%d]; give each model a unique name", model.Name, first)
		} else {
			firstUse[model.Name] = i
		}
	}
	switch llm.Schedule {
	case ScheduleOrdered, ScheduleRoundRobin, ScheduleSticky:
	default:
		add("llm.schedule", "must be one of %q, %q, %q; got %q", ScheduleOrdered, ScheduleRoundRobin, ScheduleSticky, llm.Schedule)
	}
	switch {
	case len(llm.Models) == 0:
	case llm.MaxModelRetry <= 0:
		add("llm.max_model_retry", "must be positive")
	case llm.MaxModelRetry > len(llm.Models):
		add("llm.max_model_retry", "must not exceed the %d configured models, got %d", len(llm.Models), llm.MaxModelRetry)
	}
}

func validateModel(prefix string, model Model, add addFunc) {
	if model.BaseURL == "" {
		add(prefix+".base_url", "required")
	} else if !isHTTPURL(model.BaseURL) {
		add(prefix+".base_url", "must be an absolute http or https URL, got %q", model.BaseURL)
	}
	if model.Model == "" {
		add(prefix+".model", "required")
	}
	if model.APIKeyEnv != "" {
		if !envNamePattern.MatchString(model.APIKeyEnv) {
			add(prefix+".api_key_env", "invalid environment variable name %q", model.APIKeyEnv)
		} else if isPlaintextRemote(model.BaseURL) {
			add(prefix+".api_key_env", "requires an https base_url unless the host is loopback, got %q", model.BaseURL)
		}
	}
	if model.Timeout < 0 {
		add(prefix+".timeout", "must not be negative")
	}
	for i, entry := range model.RetryHTTPCodes {
		if !validStatusEntry(entry) {
			add(prefix+".retry_http_codes", "entry %d must be a status from 400 to 599, 4xx, or 5xx; got %q", i, entry)
		}
	}
	validateSampling(prefix+".sampling", model.Sampling, add)
}

func validStatusEntry(entry string) bool {
	if entry == "4xx" || entry == "5xx" {
		return true
	}
	code, err := strconv.Atoi(entry)
	return err == nil && code >= 400 && code <= 599
}

func validateSampling(prefix string, s Sampling, add addFunc) {
	inRange := []struct {
		field  string
		value  *float64
		lo, hi float64
	}{
		{".temperature", s.Temperature, 0, 2},
		{".top_p", s.TopP, 0, 1},
		{".min_p", s.MinP, 0, 1},
		{".frequency_penalty", s.FrequencyPenalty, -2, 2},
		{".presence_penalty", s.PresencePenalty, -2, 2},
	}
	for _, r := range inRange {
		if r.value != nil && (*r.value < r.lo || *r.value > r.hi) {
			add(prefix+r.field, "must be between %g and %g, got %g", r.lo, r.hi, *r.value)
		}
	}
	if s.RepetitionPenalty != nil && *s.RepetitionPenalty <= 0 {
		add(prefix+".repetition_penalty", "must be positive")
	}
	if s.TopK != nil && *s.TopK < 0 {
		add(prefix+".top_k", "must not be negative")
	}
	if s.MaxTokens != nil && *s.MaxTokens <= 0 {
		add(prefix+".max_tokens", "must be positive")
	}
	for i, stop := range s.Stop {
		if stop == "" {
			add(prefix+".stop", "entry %d is empty", i)
		}
	}
}
