package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const fullConfig = `
llm:
  base_url: https://llama-cpp.homelab/v1
  model: qwen3-0b6
  api_key_env: LLM_API_KEY
  disable_thinking: true
mcp:
  servers:
    builtin:
      transport: stdio
      command: /usr/bin/chatops-mcp
      args: [serve, --tools, ping]
      env:
        KUBECONFIG: /etc/chatops/kubeconfig
      secret_env:
        VAULT_TOKEN: CHATOPS_VAULT_TOKEN
    monitoring:
      transport: streamable-http
      url: https://monitoring.example.internal/mcp
      bearer_token_env: MONITORING_MCP_TOKEN
agent:
  max_iterations: 4
  turn_timeout: 1m
  tool_timeout: 10s
  max_tool_result_bytes: 1024
  history_turns: 5
  history_ttl: 1h
  max_concurrent_turns: 2
  max_pending_messages: 16
`

func Test_Parse_full(t *testing.T) {
	cfg, err := Parse([]byte(fullConfig))
	require.NoError(t, err)
	require.Equal(t, &Config{
		LLM: LLM{
			BaseURL:         "https://llama-cpp.homelab/v1",
			Model:           "qwen3-0b6",
			APIKeyEnv:       "LLM_API_KEY",
			DisableThinking: true,
		},
		MCP: MCP{Servers: map[string]Server{
			"builtin": {
				Transport: TransportStdio,
				Command:   "/usr/bin/chatops-mcp",
				Args:      []string{"serve", "--tools", "ping"},
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
			MaxIterations:      4,
			TurnTimeout:        time.Minute,
			ToolTimeout:        10 * time.Second,
			MaxToolResultBytes: 1024,
			HistoryTurns:       5,
			HistoryTTL:         time.Hour,
			MaxConcurrentTurns: 2,
			MaxPendingMessages: 16,
		},
	}, cfg)
}

func Test_Parse_defaults(t *testing.T) {
	cfg, err := Parse([]byte("llm:\n  base_url: http://localhost:8080/v1\n  model: m\n"))
	require.NoError(t, err)
	require.Equal(t, Agent{
		MaxIterations:      8,
		TurnTimeout:        120 * time.Second,
		ToolTimeout:        30 * time.Second,
		MaxToolResultBytes: 65536,
		HistoryTurns:       20,
		HistoryTTL:         24 * time.Hour,
		MaxConcurrentTurns: 4,
		MaxPendingMessages: 64,
	}, cfg.Agent)
	require.Empty(t, cfg.MCP.Servers)
}

func Test_Parse_single_document_marker(t *testing.T) {
	cfg, err := Parse([]byte("---\nllm:\n  base_url: http://localhost:8080/v1\n  model: m\n"))
	require.NoError(t, err)
	require.Equal(t, "m", cfg.LLM.Model)
}

func Test_Parse_errors(t *testing.T) {
	tests := map[string]struct {
		input  string
		errMsg string
	}{
		"unknown-field": {
			input:  "llm:\n  base_url: http://x/v1\n  model: m\n  temperature: 1\n",
			errMsg: "field temperature not found",
		},
		"bad-duration": {
			input:  "llm:\n  base_url: http://x/v1\n  model: m\nagent:\n  turn_timeout: soon\n",
			errMsg: "parse config",
		},
		"not-yaml": {
			input:  "llm: [",
			errMsg: "parse config",
		},
		"two-documents": {
			input:  "llm:\n  base_url: http://x/v1\n  model: m\n---\nllm:\n  model: other\n",
			errMsg: "parse config: multiple YAML documents are not supported",
		},
		"trailing-empty-document": {
			input:  "llm:\n  base_url: http://x/v1\n  model: m\n---\n",
			errMsg: "parse config: multiple YAML documents are not supported",
		},
		"bad-second-document": {
			input:  "llm:\n  base_url: http://x/v1\n  model: m\n---\nllm: [\n",
			errMsg: "parse config: multiple YAML documents are not supported",
		},
		"empty": {
			input:  "",
			errMsg: "llm.base_url: required",
		},
		"invalid": {
			input:  "llm:\n  model: m\n",
			errMsg: "llm.base_url: required",
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
	require.Equal(t, "qwen3-0b6", cfg.LLM.Model)

	_, err = Load(filepath.Join(dir, "missing.yaml"))
	require.ErrorContains(t, err, "read config")
}
