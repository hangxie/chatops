// Package config loads and validates the chatops daemon configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// MCP server transports.
const (
	TransportStdio          = "stdio"
	TransportStreamableHTTP = "streamable-http"
)

// Config is the daemon configuration; secrets live in the environment variables named by *Env fields.
type Config struct {
	Chat  Chat  `yaml:"chat"`
	LLM   LLM   `yaml:"llm"`
	MCP   MCP   `yaml:"mcp"`
	Agent Agent `yaml:"agent"`
}

// Chat configures chat backends; `chatops serve` requires Slack, the terminal harness needs none.
type Chat struct {
	Slack *Slack `yaml:"slack"`
}

// Slack names the environment variables holding the bot (xoxb) and app-level (xapp) tokens.
type Slack struct {
	BotTokenEnv string `yaml:"bot_token_env"`
	AppTokenEnv string `yaml:"app_token_env"`
}

// LLM configures the OpenAI-compatible model endpoint.
type LLM struct {
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
	// DisableThinking stops small reasoning models from spending the budget on thinking.
	DisableThinking bool `yaml:"disable_thinking"`
}

// MCP lists the administrator-configured MCP servers, keyed by server ID.
type MCP struct {
	Servers map[string]Server `yaml:"servers"`
}

// Server configures one MCP server connection.
type Server struct {
	Transport string `yaml:"transport"`

	// stdio
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	// SecretEnv maps a child variable to the daemon variable holding its secret value.
	SecretEnv map[string]string `yaml:"secret_env"`

	// streamable-http
	URL            string `yaml:"url"`
	BearerTokenEnv string `yaml:"bearer_token_env"`
}

// Agent bounds a single conversation turn, its history, and how many turns run at once.
type Agent struct {
	// SystemPrompt is the agent's instructions; required, so startup fails if it is empty.
	SystemPrompt  string        `yaml:"system_prompt"`
	MaxIterations int           `yaml:"max_iterations"`
	TurnTimeout   time.Duration `yaml:"turn_timeout"`
	ToolTimeout   time.Duration `yaml:"tool_timeout"`
	// MaxToolResultBytes bounds tool output; the truncation marker is added on top.
	MaxToolResultBytes int `yaml:"max_tool_result_bytes"`
	// HistoryTurns is how many earlier user messages to replay as context; 0 (the default) makes each request stateless.
	HistoryTurns int           `yaml:"history_turns"`
	HistoryTTL   time.Duration `yaml:"history_ttl"`
	// MaxConcurrentTurns bounds turns running at once across all conversations.
	MaxConcurrentTurns int `yaml:"max_concurrent_turns"`
	// MaxPendingMessages bounds accepted but unfinished messages; more are refused as busy.
	MaxPendingMessages int `yaml:"max_pending_messages"`
}

func defaultAgent() Agent {
	return Agent{
		MaxIterations:      8,
		TurnTimeout:        120 * time.Second,
		ToolTimeout:        30 * time.Second,
		MaxToolResultBytes: 64 * 1024,
		HistoryTurns:       0,
		HistoryTTL:         24 * time.Hour,
		MaxConcurrentTurns: 4,
		MaxPendingMessages: 64,
	}
}

// Load reads, parses, and validates the YAML config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes, defaults, and validates; unknown fields are rejected so typos are not ignored.
func Parse(data []byte) (*Config, error) {
	cfg := Config{Agent: defaultAgent()}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	err := decoder.Decode(&cfg)
	switch {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, fmt.Errorf("parse config: %w", err)
	default:
		// Later documents would be ignored, so a stray "---" must not hide settings.
		if err := decoder.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
			return nil, errors.New("parse config: multiple YAML documents are not supported")
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	return &cfg, nil
}
