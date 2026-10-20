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
	Chat    Chat    `yaml:"chat"`
	LLM     LLM     `yaml:"llm"`
	MCP     MCP     `yaml:"mcp"`
	Agent   Agent   `yaml:"agent"`
	Metrics Metrics `yaml:"metrics"`
}

// Metrics configures the Prometheus and health probe endpoint.
type Metrics struct {
	// Listen is the host:port serving /metrics, /healthz, and /readyz; empty disables the endpoint.
	Listen string `yaml:"listen"`
}

// Chat backend types.
const (
	ChatSlack   = "slack"
	ChatDiscord = "discord"
	ChatStdio   = "stdio"
	ChatUnix    = "unix"
)

// Reply modes: where slack and discord answer a top-level message.
const (
	ReplyThread  = "thread"
	ReplyChannel = "channel"
)

// Chat maps a backend name to its definition; `chatops serve` runs every one.
type Chat map[string]ChatBackend

// ChatBackend configures one chat backend; Type selects the implementation and the fields it reads.
type ChatBackend struct {
	Type string `yaml:"type"`

	// slack: environment variables holding the bot (xoxb) and app-level (xapp) tokens; discord: the bot token.
	BotTokenEnv string `yaml:"bot_token_env"`
	AppTokenEnv string `yaml:"app_token_env"`
	// ReplyMode is ReplyThread or ReplyChannel; empty takes the backend's default (slack thread, discord channel).
	ReplyMode string `yaml:"reply_mode"`

	// unix: filesystem path of the socket to create, and optional octal permission bits (e.g. "0600").
	Path string `yaml:"path"`
	Mode string `yaml:"mode"`
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
	// ShowStats appends each turn's step timings, tokens, and sizes to its reply.
	ShowStats bool `yaml:"show_stats"`
	// RequireToolCall forces a tool call, the built-in decline included, on each turn's first model call.
	RequireToolCall bool `yaml:"require_tool_call"`
	// DeclineReply is posted when the model declines a message as off-topic.
	DeclineReply string `yaml:"decline_reply"`
}

// DefaultDeclineReply is the off-topic reply when agent.decline_reply is unset.
const DefaultDeclineReply = "I can only help with operations questions about the systems I monitor."

func defaultAgent() Agent {
	return Agent{
		MaxIterations:      20,
		TurnTimeout:        120 * time.Second,
		ToolTimeout:        30 * time.Second,
		MaxToolResultBytes: 64 * 1024,
		HistoryTurns:       0,
		HistoryTTL:         24 * time.Hour,
		MaxConcurrentTurns: 4,
		MaxPendingMessages: 64,
		RequireToolCall:    true,
		DeclineReply:       DefaultDeclineReply,
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
	cfg.LLM = cfg.LLM.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	return &cfg, nil
}
