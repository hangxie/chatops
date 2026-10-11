package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	// No "_" in server IDs keeps the "<server>__<tool>" separator unambiguous.
	serverIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	envNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Validate reports every configuration problem, one error per field.
func (c Config) Validate() error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", field, fmt.Sprintf(format, args...)))
	}

	if c.Chat.Slack != nil {
		requireEnvName("chat.slack.bot_token_env", c.Chat.Slack.BotTokenEnv, add)
		requireEnvName("chat.slack.app_token_env", c.Chat.Slack.AppTokenEnv, add)
	}
	validateLLM(c.LLM, add)
	ids := make([]string, 0, len(c.MCP.Servers))
	for id := range c.MCP.Servers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		validateServer(id, c.MCP.Servers[id], add)
	}
	validateAgent(c.Agent, add)
	return errors.Join(errs...)
}

type addFunc func(field, format string, args ...any)

func requireEnvName(field, name string, add addFunc) {
	switch {
	case name == "":
		add(field, "required")
	case !envNamePattern.MatchString(name):
		add(field, "invalid environment variable name %q", name)
	}
}

func validateLLM(llm LLM, add addFunc) {
	if llm.BaseURL == "" {
		add("llm.base_url", "required")
	} else if !isHTTPURL(llm.BaseURL) {
		add("llm.base_url", "must be an absolute http or https URL, got %q", llm.BaseURL)
	}
	if llm.Model == "" {
		add("llm.model", "required")
	}
	if llm.APIKeyEnv != "" {
		if !envNamePattern.MatchString(llm.APIKeyEnv) {
			add("llm.api_key_env", "invalid environment variable name %q", llm.APIKeyEnv)
		} else if isPlaintextRemote(llm.BaseURL) {
			add("llm.api_key_env", "requires an https base_url unless the host is loopback, got %q", llm.BaseURL)
		}
	}
	validateSampling(llm.Sampling, add)
}

func validateSampling(s Sampling, add addFunc) {
	inRange := []struct {
		field  string
		value  *float64
		lo, hi float64
	}{
		{"llm.sampling.temperature", s.Temperature, 0, 2},
		{"llm.sampling.top_p", s.TopP, 0, 1},
		{"llm.sampling.min_p", s.MinP, 0, 1},
		{"llm.sampling.frequency_penalty", s.FrequencyPenalty, -2, 2},
		{"llm.sampling.presence_penalty", s.PresencePenalty, -2, 2},
	}
	for _, r := range inRange {
		if r.value != nil && (*r.value < r.lo || *r.value > r.hi) {
			add(r.field, "must be between %g and %g, got %g", r.lo, r.hi, *r.value)
		}
	}
	if s.RepetitionPenalty != nil && *s.RepetitionPenalty <= 0 {
		add("llm.sampling.repetition_penalty", "must be positive")
	}
	if s.TopK != nil && *s.TopK < 0 {
		add("llm.sampling.top_k", "must not be negative")
	}
	if s.MaxTokens != nil && *s.MaxTokens <= 0 {
		add("llm.sampling.max_tokens", "must be positive")
	}
	for i, stop := range s.Stop {
		if stop == "" {
			add("llm.sampling.stop", "entry %d is empty", i)
		}
	}
}

func validateServer(id string, server Server, add addFunc) {
	prefix := "mcp.servers." + id
	if !serverIDPattern.MatchString(id) {
		add(prefix, "id must match %s", serverIDPattern)
		return
	}
	switch server.Transport {
	case TransportStdio:
		if server.Command == "" {
			add(prefix+".command", "required for stdio")
		}
		if server.URL != "" {
			add(prefix+".url", "not allowed for stdio")
		}
		if server.BearerTokenEnv != "" {
			add(prefix+".bearer_token_env", "not allowed for stdio")
		}
		for _, name := range sortedKeys(server.Env) {
			if !envNamePattern.MatchString(name) {
				add(prefix+".env", "invalid environment variable name %q", name)
			}
		}
		validateSecretEnv(prefix, server, add)
	case TransportStreamableHTTP:
		if server.URL == "" {
			add(prefix+".url", "required for streamable-http")
		} else if !isHTTPURL(server.URL) {
			add(prefix+".url", "must be an absolute http or https URL, got %q", server.URL)
		}
		if server.Command != "" {
			add(prefix+".command", "not allowed for streamable-http")
		}
		if len(server.Args) != 0 {
			add(prefix+".args", "not allowed for streamable-http")
		}
		if len(server.Env) != 0 {
			add(prefix+".env", "not allowed for streamable-http")
		}
		if len(server.SecretEnv) != 0 {
			add(prefix+".secret_env", "not allowed for streamable-http")
		}
		if server.BearerTokenEnv != "" {
			if !envNamePattern.MatchString(server.BearerTokenEnv) {
				add(prefix+".bearer_token_env", "invalid environment variable name %q", server.BearerTokenEnv)
			} else if isPlaintextRemote(server.URL) {
				add(prefix+".bearer_token_env", "requires an https url unless the host is loopback, got %q", server.URL)
			}
		}
	default:
		add(prefix+".transport", "must be %q or %q, got %q", TransportStdio, TransportStreamableHTTP, server.Transport)
	}
}

func validateSecretEnv(prefix string, server Server, add addFunc) {
	for _, name := range sortedKeys(server.SecretEnv) {
		source := server.SecretEnv[name]
		switch {
		case !envNamePattern.MatchString(name):
			add(prefix+".secret_env", "invalid environment variable name %q", name)
		case !envNamePattern.MatchString(source):
			add(prefix+".secret_env."+name, "invalid environment variable name %q", source)
		default:
			if _, ok := server.Env[name]; ok {
				add(prefix+".secret_env."+name, "also set in env")
			}
		}
	}
}

func validateAgent(agent Agent, add addFunc) {
	if strings.TrimSpace(agent.SystemPrompt) == "" {
		add("agent.system_prompt", "required")
	}
	positive := []struct {
		field string
		ok    bool
	}{
		{"agent.max_iterations", agent.MaxIterations > 0},
		{"agent.turn_timeout", agent.TurnTimeout > 0},
		{"agent.tool_timeout", agent.ToolTimeout > 0},
		{"agent.max_tool_result_bytes", agent.MaxToolResultBytes > 0},
		{"agent.history_ttl", agent.HistoryTTL > 0},
		{"agent.max_concurrent_turns", agent.MaxConcurrentTurns > 0},
		{"agent.max_pending_messages", agent.MaxPendingMessages > 0},
	}
	for _, p := range positive {
		if !p.ok {
			add(p.field, "must be positive")
		}
	}
	if agent.HistoryTurns < 0 {
		add("agent.history_turns", "must not be negative")
	}
	if agent.TurnTimeout > 0 && agent.ToolTimeout > agent.TurnTimeout {
		add("agent.tool_timeout", "must not exceed agent.turn_timeout")
	}
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}

// isPlaintextRemote reports whether raw is plain http to a non-loopback host.
func isPlaintextRemote(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
