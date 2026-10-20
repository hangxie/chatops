package config

import (
	"slices"
	"strconv"
	"time"
)

// Schedules: how a turn picks its first model.
const (
	ScheduleOrdered    = "ordered"
	ScheduleRoundRobin = "round_robin"
	ScheduleSticky     = "sticky"
)

// LLM lists the model endpoints and how a turn picks and fails over among them.
type LLM struct {
	Models   []Model `yaml:"models"`
	Schedule string  `yaml:"schedule"`
	// MaxModelRetry caps the models tried per model call; unset means every model once.
	MaxModelRetry int `yaml:"max_model_retry"`
}

// Model configures one OpenAI-compatible model endpoint.
type Model struct {
	// Name identifies the model in logs, metrics, and reply stats; it defaults to Model and must be unique.
	Name      string `yaml:"name"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
	// DisableThinking stops small reasoning models from spending the budget on thinking.
	DisableThinking bool `yaml:"disable_thinking"`
	// Stream lets a proxy see a timed-out request disconnect and cancel generation upstream; nil means on.
	Stream *bool `yaml:"stream"`
	// Timeout bounds one call to this model, so a hung endpoint leaves turn time for the next; 0 means none.
	Timeout time.Duration `yaml:"timeout"`
	// RetryHTTPCodes lists the statuses (or 4xx, 5xx) that move a call to the next model; nil means 429 and 5xx.
	RetryHTTPCodes []string `yaml:"retry_http_codes"`
	Sampling       Sampling `yaml:"sampling"`
}

// RetriesStatus reports whether an HTTP status from this model moves the call to the next model.
func (m Model) RetriesStatus(code int) bool {
	if m.RetryHTTPCodes == nil {
		return code == 429 || code >= 500
	}
	return slices.ContainsFunc(m.RetryHTTPCodes, func(entry string) bool { return statusMatches(entry, code) })
}

// statusMatches reports whether code is entry, an exact status or a class such as 5xx.
func statusMatches(entry string, code int) bool {
	if len(entry) == 3 && entry[1:] == "xx" {
		return entry[:1] == strconv.Itoa(code/100)
	}
	return entry == strconv.Itoa(code)
}

// Streaming reports whether completions are streamed, which is the default.
func (m Model) Streaming() bool {
	return m.Stream == nil || *m.Stream
}

// Sampling holds optional generation parameters; a nil field is omitted so the endpoint default applies.
// top_k, min_p, and repetition_penalty are vLLM/llama.cpp extensions that strict OpenAI endpoints reject.
type Sampling struct {
	Temperature       *float64 `yaml:"temperature"`
	TopP              *float64 `yaml:"top_p"`
	TopK              *int     `yaml:"top_k"`
	MinP              *float64 `yaml:"min_p"`
	MaxTokens         *int     `yaml:"max_tokens"`
	FrequencyPenalty  *float64 `yaml:"frequency_penalty"`
	PresencePenalty   *float64 `yaml:"presence_penalty"`
	RepetitionPenalty *float64 `yaml:"repetition_penalty"`
	Seed              *int     `yaml:"seed"`
	Stop              []string `yaml:"stop"`
}

func (l LLM) withDefaults() LLM {
	if l.Schedule == "" {
		l.Schedule = ScheduleOrdered
	}
	if l.MaxModelRetry == 0 {
		l.MaxModelRetry = len(l.Models)
	}
	l.Models = slices.Clone(l.Models)
	for i := range l.Models {
		if l.Models[i].Name == "" {
			l.Models[i].Name = l.Models[i].Model
		}
	}
	return l
}
