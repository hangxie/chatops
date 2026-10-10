package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validConfig() Config {
	return Config{
		LLM: LLM{BaseURL: "http://localhost:8080/v1", Model: "m"},
		MCP: MCP{Servers: map[string]Server{
			"builtin": {Transport: TransportStdio, Command: "chatops-mcp"},
			"remote":  {Transport: TransportStreamableHTTP, URL: "https://example.internal/mcp"},
		}},
		Agent: defaultAgent(),
	}
}

func Test_Config_Validate(t *testing.T) {
	tests := map[string]struct {
		mutate func(*Config)
		errs   []string
	}{
		"valid":             {mutate: func(*Config) {}},
		"slack":             {mutate: func(c *Config) { c.Chat.Slack = &Slack{BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN"} }},
		"slack-missing-env": {mutate: func(c *Config) { c.Chat.Slack = &Slack{} }, errs: []string{"chat.slack.bot_token_env: required", "chat.slack.app_token_env: required"}},
		"slack-bad-env": {mutate: func(c *Config) { c.Chat.Slack = &Slack{BotTokenEnv: "1BOT", AppTokenEnv: "A-B"} }, errs: []string{
			`chat.slack.bot_token_env: invalid environment variable name "1BOT"`,
			`chat.slack.app_token_env: invalid environment variable name "A-B"`,
		}},
		"no-servers":        {mutate: func(c *Config) { c.MCP.Servers = nil }},
		"missing-base-url":  {mutate: func(c *Config) { c.LLM.BaseURL = "" }, errs: []string{"llm.base_url: required"}},
		"relative-base-url": {mutate: func(c *Config) { c.LLM.BaseURL = "localhost/v1" }, errs: []string{`llm.base_url: must be an absolute http or https URL, got "localhost/v1"`}},
		"ftp-base-url":      {mutate: func(c *Config) { c.LLM.BaseURL = "ftp://host/v1" }, errs: []string{`llm.base_url: must be an absolute http or https URL, got "ftp://host/v1"`}},
		"missing-model":     {mutate: func(c *Config) { c.LLM.Model = "" }, errs: []string{"llm.model: required"}},
		"bad-api-key-env":   {mutate: func(c *Config) { c.LLM.APIKeyEnv = "1KEY" }, errs: []string{`llm.api_key_env: invalid environment variable name "1KEY"`}},
		"good-api-key-env":  {mutate: func(c *Config) { c.LLM.APIKeyEnv = "LLM_API_KEY" }},
		"bad-server-id":     {mutate: func(c *Config) { c.MCP.Servers["Bad_ID"] = Server{Transport: TransportStdio, Command: "x"} }, errs: []string{`mcp.servers.Bad_ID: id must match ^[a-z][a-z0-9-]{0,31}$`}},
		"missing-transport": {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Command: "x"} }, errs: []string{`mcp.servers.x.transport: must be "stdio" or "streamable-http", got ""`}},
		"unknown-transport": {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Transport: "sse"} }, errs: []string{`mcp.servers.x.transport: must be "stdio" or "streamable-http", got "sse"`}},
		"stdio-no-command":  {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Transport: TransportStdio} }, errs: []string{"mcp.servers.x.command: required for stdio"}},
		"stdio-with-url":    {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Transport: TransportStdio, Command: "x", URL: "http://h"} }, errs: []string{"mcp.servers.x.url: not allowed for stdio"}},
		"stdio-with-bearer": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStdio, Command: "x", BearerTokenEnv: "T"}
		}, errs: []string{"mcp.servers.x.bearer_token_env: not allowed for stdio"}},
		"stdio-bad-env-name": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStdio, Command: "x", Env: map[string]string{"A=B": "c"}}
		}, errs: []string{`mcp.servers.x.env: invalid environment variable name "A=B"`}},
		"http-no-url":  {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP} }, errs: []string{"mcp.servers.x.url: required for streamable-http"}},
		"http-bad-url": {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "unix:///sock"} }, errs: []string{`mcp.servers.x.url: must be an absolute http or https URL, got "unix:///sock"`}},
		"http-with-command": {
			mutate: func(c *Config) {
				c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "http://h/mcp", Command: "x", Args: []string{"a"}, Env: map[string]string{"A": "b"}}
			},
			errs: []string{
				"mcp.servers.x.command: not allowed for streamable-http",
				"mcp.servers.x.args: not allowed for streamable-http",
				"mcp.servers.x.env: not allowed for streamable-http",
			},
		},
		"http-bad-bearer": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "https://h/mcp", BearerTokenEnv: "a-b"}
		}, errs: []string{`mcp.servers.x.bearer_token_env: invalid environment variable name "a-b"`}},
		"http-bearer-plaintext": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "http://h/mcp", BearerTokenEnv: "T"}
		}, errs: []string{`mcp.servers.x.bearer_token_env: requires an https url unless the host is loopback, got "http://h/mcp"`}},
		"http-bearer-plaintext-upper": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "HTTP://h/mcp", BearerTokenEnv: "T"}
		}, errs: []string{`mcp.servers.x.bearer_token_env: requires an https url unless the host is loopback, got "HTTP://h/mcp"`}},
		"http-bearer-loopback-v4": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "http://127.0.0.1:8080/mcp", BearerTokenEnv: "T"}
		}},
		"http-bearer-loopback-v6": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "http://[::1]:8080/mcp", BearerTokenEnv: "T"}
		}},
		"http-bearer-localhost": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "http://localhost:8080/mcp", BearerTokenEnv: "T"}
		}},
		"http-bearer-localhost-lookalike": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "http://localhost.example.com/mcp", BearerTokenEnv: "T"}
		}, errs: []string{`mcp.servers.x.bearer_token_env: requires an https url unless the host is loopback, got "http://localhost.example.com/mcp"`}},
		"https-bearer": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "https://h/mcp", BearerTokenEnv: "T"}
		}},
		"http-no-bearer": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "http://h/mcp"}
		}},
		"api-key-plaintext": {mutate: func(c *Config) {
			c.LLM = LLM{BaseURL: "http://llm.example.internal/v1", Model: "m", APIKeyEnv: "LLM_API_KEY"}
		}, errs: []string{`llm.api_key_env: requires an https base_url unless the host is loopback, got "http://llm.example.internal/v1"`}},
		"api-key-https": {mutate: func(c *Config) {
			c.LLM = LLM{BaseURL: "https://llm.example.internal/v1", Model: "m", APIKeyEnv: "LLM_API_KEY"}
		}},
		"stdio-secret-env": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStdio, Command: "x", SecretEnv: map[string]string{"VAULT_TOKEN": "CHATOPS_VAULT_TOKEN"}}
		}},
		"stdio-secret-env-bad-names": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStdio, Command: "x", SecretEnv: map[string]string{"A=B": "SRC", "OK": "1BAD"}}
		}, errs: []string{
			`mcp.servers.x.secret_env: invalid environment variable name "A=B"`,
			`mcp.servers.x.secret_env.OK: invalid environment variable name "1BAD"`,
		}},
		"stdio-secret-env-overlaps-env": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStdio, Command: "x", Env: map[string]string{"TOKEN": "plain"}, SecretEnv: map[string]string{"TOKEN": "SRC"}}
		}, errs: []string{`mcp.servers.x.secret_env.TOKEN: also set in env`}},
		"http-with-secret-env": {mutate: func(c *Config) {
			c.MCP.Servers["x"] = Server{Transport: TransportStreamableHTTP, URL: "https://h/mcp", SecretEnv: map[string]string{"A": "B"}}
		}, errs: []string{"mcp.servers.x.secret_env: not allowed for streamable-http"}},
		"agent-non-positive": {
			mutate: func(c *Config) {
				c.Agent = Agent{MaxIterations: -1, MaxToolResultBytes: -1, HistoryTurns: -1, TurnTimeout: -time.Second, ToolTimeout: -time.Second, HistoryTTL: -time.Second, MaxConcurrentTurns: -1, MaxPendingMessages: -1}
			},
			errs: []string{
				"agent.max_iterations: must be positive",
				"agent.turn_timeout: must be positive",
				"agent.tool_timeout: must be positive",
				"agent.max_tool_result_bytes: must be positive",
				"agent.history_turns: must be positive",
				"agent.history_ttl: must be positive",
				"agent.max_concurrent_turns: must be positive",
				"agent.max_pending_messages: must be positive",
			},
		},
		"tool-timeout-exceeds-turn": {
			mutate: func(c *Config) { c.Agent.ToolTimeout = c.Agent.TurnTimeout + time.Second },
			errs:   []string{"agent.tool_timeout: must not exceed agent.turn_timeout"},
		},
		"servers-sorted": {
			mutate: func(c *Config) {
				c.MCP.Servers["zz"] = Server{}
				c.MCP.Servers["aa"] = Server{}
			},
			errs: []string{
				`mcp.servers.aa.transport: must be "stdio" or "streamable-http", got ""`,
				`mcp.servers.zz.transport: must be "stdio" or "streamable-http", got ""`,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
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

func splitErrors(err error) []string {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []string{err.Error()}
	}
	var msgs []string
	for _, e := range joined.Unwrap() {
		msgs = append(msgs, e.Error())
	}
	return msgs
}
