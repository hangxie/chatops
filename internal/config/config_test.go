package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const fullConfig = `
chat:
  slack:
    type: slack
    bot_token_env: SLACK_BOT_TOKEN
    app_token_env: SLACK_APP_TOKEN
llm:
  models:
    - name: qwen3-0b6
      base_url: https://llama-cpp.homelab/v1
      model: qwen3-0b6
      api_key_env: LLM_API_KEY
      disable_thinking: true
      timeout: 20s
    - base_url: https://openrouter.ai/api/v1
      name: openrouter
      model: nvidia/nemotron
      api_key_env: OR_API_KEY
      stream: false
      retry_http_codes: [401, 403, 5xx]
  schedule: sticky
  max_model_retry: 2
mcp:
  servers:
    builtin:
      transport: stdio
      command: /usr/bin/chatops-mcp
      args: [serve, --tool, ping]
      env:
        KUBECONFIG: /etc/chatops/kubeconfig
      secret_env:
        VAULT_TOKEN: CHATOPS_VAULT_TOKEN
    monitoring:
      transport: streamable-http
      url: https://monitoring.example.internal/mcp
      bearer_token_env: MONITORING_MCP_TOKEN
agent:
  system_prompt: "Answer tersely."
  max_iterations: 4
  turn_timeout: 1m
  tool_timeout: 10s
  max_tool_result_bytes: 1024
  history_turns: 5
  history_ttl: 1h
  max_concurrent_turns: 2
  max_pending_messages: 16
  show_stats: true
  require_tool_call: false
  decline_reply: "Out of scope."
metrics:
  listen: 127.0.0.1:9464
`

func Test_Parse_full(t *testing.T) {
	cfg, err := Parse([]byte(fullConfig))
	require.NoError(t, err)
	require.Equal(t, &Config{
		Chat: Chat{"slack": {Type: ChatSlack, BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN"}},
		LLM: LLM{
			Models: []Model{
				{BaseURL: "https://llama-cpp.homelab/v1", Model: "qwen3-0b6", Name: "qwen3-0b6", APIKeyEnv: "LLM_API_KEY", DisableThinking: true, Timeout: 20 * time.Second},
				{BaseURL: "https://openrouter.ai/api/v1", Model: "nvidia/nemotron", Name: "openrouter", APIKeyEnv: "OR_API_KEY", Stream: pval(false), RetryHTTPCodes: []string{"401", "403", "5xx"}},
			},
			Schedule:      ScheduleSticky,
			MaxModelRetry: 2,
		},
		MCP: MCP{Servers: map[string]Server{
			"builtin": {
				Transport: TransportStdio,
				Command:   "/usr/bin/chatops-mcp",
				Args:      []string{"serve", "--tool", "ping"},
				Env:       map[string]string{"KUBECONFIG": "/etc/chatops/kubeconfig"},
				SecretEnv: map[string]string{"VAULT_TOKEN": "CHATOPS_VAULT_TOKEN"},
			},
			"monitoring": {
				Transport:      TransportStreamableHTTP,
				URL:            "https://monitoring.example.internal/mcp",
				BearerTokenEnv: "MONITORING_MCP_TOKEN",
			},
		}},
		Agent: Agent{
			SystemPrompt:       "Answer tersely.",
			MaxIterations:      4,
			TurnTimeout:        time.Minute,
			ToolTimeout:        10 * time.Second,
			MaxToolResultBytes: 1024,
			HistoryTurns:       5,
			HistoryTTL:         time.Hour,
			MaxConcurrentTurns: 2,
			MaxPendingMessages: 16,
			ShowStats:          true,
			RequireToolCall:    false,
			DeclineReply:       "Out of scope.",
		},
		Metrics: Metrics{Listen: "127.0.0.1:9464"},
	}, cfg)
}

// minimalChat is the smallest valid chat section, since a config needs at least one backend.
const minimalChat = "chat:\n  console:\n    type: stdio\n"

func Test_Parse_defaults(t *testing.T) {
	cfg, err := Parse([]byte(minimalChat + "llm:\n  models:\n    - name: m\n      base_url: http://localhost:8080/v1\n      model: m\nagent:\n  system_prompt: p\n"))
	require.NoError(t, err)
	require.Equal(t, Agent{
		SystemPrompt:       "p",
		MaxIterations:      20,
		TurnTimeout:        120 * time.Second,
		ToolTimeout:        30 * time.Second,
		MaxToolResultBytes: 65536,
		HistoryTurns:       0,
		HistoryTTL:         24 * time.Hour,
		MaxConcurrentTurns: 4,
		MaxPendingMessages: 64,
		RequireToolCall:    true,
		DeclineReply:       DefaultDeclineReply,
	}, cfg.Agent)
	require.Equal(t, LLM{Models: []Model{{BaseURL: "http://localhost:8080/v1", Model: "m", Name: "m"}}, Schedule: ScheduleOrdered, MaxModelRetry: 1}, cfg.LLM)
	require.Empty(t, cfg.MCP.Servers)
}

func Test_Parse_stream(t *testing.T) {
	tests := map[string]struct {
		yaml string
		want bool
	}{
		"default": {yaml: "", want: true},
		"off":     {yaml: "      stream: false\n", want: false},
		"on":      {yaml: "      stream: true\n", want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(minimalChat + "llm:\n  models:\n    - name: m\n      base_url: http://localhost:8080/v1\n      model: m\n" + tc.yaml + "agent:\n  system_prompt: p\n"))
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.LLM.Models[0].Streaming())
		})
	}
}

func Test_Parse_sampling(t *testing.T) {
	cfg, err := Parse([]byte(minimalChat + "llm:\n  models:\n    - name: m\n      base_url: http://localhost:8080/v1\n      model: m\n      sampling:\n        temperature: 0\n        top_p: 0.9\n        seed: 7\n        stop: [\"</s>\"]\nagent:\n  system_prompt: p\n"))
	require.NoError(t, err)
	require.Equal(t, Sampling{Temperature: pval(0.0), TopP: pval(0.9), Seed: pval(7), Stop: []string{"</s>"}}, cfg.LLM.Models[0].Sampling)

	// Omitting the sampling block leaves every parameter unset, so none is sent.
	bare, err := Parse([]byte(minimalChat + "llm:\n  models:\n    - name: m\n      base_url: http://localhost:8080/v1\n      model: m\nagent:\n  system_prompt: p\n"))
	require.NoError(t, err)
	require.Equal(t, Sampling{}, bare.LLM.Models[0].Sampling)
}

func Test_Parse_single_document_marker(t *testing.T) {
	cfg, err := Parse([]byte("---\n" + minimalChat + "llm:\n  models:\n    - name: m\n      base_url: http://localhost:8080/v1\n      model: m\nagent:\n  system_prompt: p\n"))
	require.NoError(t, err)
	require.Equal(t, "m", cfg.LLM.Models[0].Model)
}

func Test_Parse_errors(t *testing.T) {
	tests := map[string]struct {
		input  string
		errMsg string
	}{
		"unknown-field": {
			input:  "llm:\n  models:\n    - name: m\n      base_url: http://x/v1\n      model: m\n      temperature: 1\n",
			errMsg: "field temperature not found",
		},
		"old-single-model": {
			input:  "llm:\n  base_url: http://x/v1\n  model: m\n",
			errMsg: "field base_url not found",
		},
		"bad-duration": {
			input:  "llm:\n  models:\n    - name: m\n      base_url: http://x/v1\n      model: m\nagent:\n  turn_timeout: soon\n",
			errMsg: "parse config",
		},
		"not-yaml": {
			input:  "llm: [",
			errMsg: "parse config",
		},
		"two-documents": {
			input:  "llm:\n  models:\n    - name: m\n      base_url: http://x/v1\n      model: m\n---\nllm:\n  model: other\n",
			errMsg: "parse config: multiple YAML documents are not supported",
		},
		"trailing-empty-document": {
			input:  "llm:\n  models:\n    - name: m\n      base_url: http://x/v1\n      model: m\n---\n",
			errMsg: "parse config: multiple YAML documents are not supported",
		},
		"bad-second-document": {
			input:  "llm:\n  models:\n    - name: m\n      base_url: http://x/v1\n      model: m\n---\nllm: [\n",
			errMsg: "parse config: multiple YAML documents are not supported",
		},
		"empty": {
			input:  "",
			errMsg: "llm.models: at least one model is required",
		},
		"invalid": {
			input:  "llm:\n  models:\n    - model: m\n",
			errMsg: "llm.models[0].base_url: required",
		},
		"retry-more-than-models": {
			input:  minimalChat + "llm:\n  models:\n    - name: m\n      base_url: http://x/v1\n      model: m\n  max_model_retry: 2\nagent:\n  system_prompt: p\n",
			errMsg: "llm.max_model_retry: must not exceed the 1 configured models, got 2",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.input))
			require.ErrorContains(t, err, tc.errMsg)
			require.Nil(t, cfg)
		})
	}
}

func Test_Load(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(fullConfig), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "qwen3-0b6", cfg.LLM.Models[0].Model)

	_, err = Load(filepath.Join(dir, "missing.yaml"))
	require.ErrorContains(t, err, "read config")
}

func Test_Load_packaged_sample(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "package", "systemd", "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, Chat{
		"homelab-slack":   {Type: ChatSlack, BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN", ReplyMode: ReplyThread},
		"homelab-discord": {Type: ChatDiscord, BotTokenEnv: "DISCORD_BOT_TOKEN", ReplyMode: ReplyChannel},
		"homelab-unix":    {Type: ChatUnix, Path: "/run/chatops/ops.sock", Mode: "0600"},
	}, cfg.Chat)
	// The local endpoint is the only active model: it needs no account or key, and backups are opt-in.
	require.Len(t, cfg.LLM.Models, 1)
	local := cfg.LLM.Models[0]
	require.Equal(t, "http://localhost:8080/v1", local.BaseURL)
	require.Empty(t, local.APIKeyEnv)
	require.Equal(t, ScheduleOrdered, cfg.LLM.Schedule)
	require.Equal(t, 1, cfg.LLM.MaxModelRetry)
	// Each built-in group runs as its own server named after the group, so tool prefixes name the group.
	require.Len(t, cfg.MCP.Servers, 4)
	for _, group := range []string{"ping", "status", "kubernetes", "host"} {
		server := cfg.MCP.Servers[group]
		require.Equal(t, "/usr/bin/chatops-mcp", server.Command, group)
		args := []string{"serve", "--tool", group}
		if group != "ping" {
			args = append(args, "--config", "/etc/chatops/mcp-"+group+".d")
		}
		require.Equal(t, args, server.Args, group)
	}
	// The shipped prompt must carry the behavioral guard, and thinking is disabled by both config and prompt.
	require.Contains(t, cfg.Agent.SystemPrompt, "Answer only the latest user request")
	require.Contains(t, cfg.Agent.SystemPrompt, "/no_think")
	require.True(t, local.DisableThinking)
	require.True(t, local.Streaming())
	require.Equal(t, 0, cfg.Agent.HistoryTurns)
	require.NotNil(t, local.Sampling.Temperature)
	require.Equal(t, 0.0, *local.Sampling.Temperature)
	// Metrics and health probes are on, bound to loopback.
	require.Equal(t, Metrics{Listen: "127.0.0.1:9464"}, cfg.Metrics)
}

func Test_Load_packaged_sample_agent_defaults(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "package", "systemd", "config.yaml"))
	require.NoError(t, err)
	// Uncommenting the agent limits shown with their defaults must not change the parsed config.
	uncommented := regexp.MustCompile(`(?m)^  # ((max_|turn_|tool_|history_ttl|require_tool_call|decline_reply)[a-z_]*:)`).ReplaceAll(data, []byte("  $1"))
	require.NotEqual(t, data, uncommented)

	cfg, err := Parse(uncommented)
	require.NoError(t, err)
	want := defaultAgent()
	want.SystemPrompt = cfg.Agent.SystemPrompt
	require.Equal(t, want, cfg.Agent)
}

func Test_Parse_reply_mode(t *testing.T) {
	for _, value := range []string{"", "thread", "channel"} {
		t.Run(value, func(t *testing.T) {
			input := fullConfig
			if value != "" {
				input = strings.Replace(input, "    app_token_env: SLACK_APP_TOKEN", "    app_token_env: SLACK_APP_TOKEN\n    reply_mode: "+value, 1)
			}
			cfg, err := Parse([]byte(input))
			require.NoError(t, err)
			require.Equal(t, value, cfg.Chat["slack"].ReplyMode)
		})
	}
}
