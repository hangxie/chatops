package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pval[T any](v T) *T { return &v }

func validConfig() Config {
	return Config{
		Chat: Chat{"console": {Type: ChatStdio}},
		LLM:  LLM{BaseURL: "http://localhost:8080/v1", Model: "m"},
		MCP: MCP{Servers: map[string]Server{
			"builtin": {Transport: TransportStdio, Command: "chatops-mcp"},
			"remote":  {Transport: TransportStreamableHTTP, URL: "https://example.internal/mcp"},
		}},
		Agent: func() Agent { a := defaultAgent(); a.SystemPrompt = "You are ChatOps."; return a }(),
	}
}

func Test_Config_Validate(t *testing.T) {
	tests := map[string]struct {
		mutate func(*Config)
		errs   []string
	}{
		"valid": {mutate: func(*Config) {}},
		"slack": {mutate: func(c *Config) {
			c.Chat = Chat{"slack": {Type: ChatSlack, BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN"}}
		}},
		"stdio":               {mutate: func(c *Config) { c.Chat = Chat{"console": {Type: ChatStdio}} }},
		"discord":             {mutate: func(c *Config) { c.Chat = Chat{"dc": {Type: ChatDiscord, BotTokenEnv: "DISCORD_BOT_TOKEN"}} }},
		"discord-missing-env": {mutate: func(c *Config) { c.Chat = Chat{"dc": {Type: ChatDiscord}} }, errs: []string{"chat.dc.bot_token_env: required"}},
		"discord-bad-env":     {mutate: func(c *Config) { c.Chat = Chat{"dc": {Type: ChatDiscord, BotTokenEnv: "1BOT"}} }, errs: []string{`chat.dc.bot_token_env: invalid environment variable name "1BOT"`}},
		"discord-with-app-token": {mutate: func(c *Config) {
			c.Chat = Chat{"dc": {Type: ChatDiscord, BotTokenEnv: "DISCORD_BOT_TOKEN", AppTokenEnv: "X"}}
		}, errs: []string{"chat.dc.app_token_env: not allowed for a discord backend"}},
		"slack-reply-thread": {mutate: func(c *Config) {
			c.Chat = Chat{"slack": {Type: ChatSlack, BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN", ReplyMode: ReplyThread}}
		}},
		"slack-reply-channel": {mutate: func(c *Config) {
			c.Chat = Chat{"slack": {Type: ChatSlack, BotTokenEnv: "SLACK_BOT_TOKEN", AppTokenEnv: "SLACK_APP_TOKEN", ReplyMode: ReplyChannel}}
		}},
		"discord-reply-thread": {mutate: func(c *Config) {
			c.Chat = Chat{"dc": {Type: ChatDiscord, BotTokenEnv: "DISCORD_BOT_TOKEN", ReplyMode: ReplyThread}}
		}},
		"discord-bad-reply-mode": {mutate: func(c *Config) {
			c.Chat = Chat{"dc": {Type: ChatDiscord, BotTokenEnv: "DISCORD_BOT_TOKEN", ReplyMode: "inline"}}
		}, errs: []string{`chat.dc.reply_mode: must be "thread" or "channel", got "inline"`}},
		"stdio-with-reply-mode": {mutate: func(c *Config) { c.Chat = Chat{"console": {Type: ChatStdio, ReplyMode: ReplyThread}} }, errs: []string{"chat.console.reply_mode: not allowed for a stdio backend"}},
		"unix-with-reply-mode": {mutate: func(c *Config) {
			c.Chat = Chat{"ops": {Type: ChatUnix, Path: "/run/chatops.sock", ReplyMode: ReplyChannel}}
		}, errs: []string{"chat.ops.reply_mode: not allowed for a unix backend"}},
		"metrics":           {mutate: func(c *Config) { c.Metrics.Listen = "127.0.0.1:9464" }},
		"metrics-any-host":  {mutate: func(c *Config) { c.Metrics.Listen = ":9464" }},
		"metrics-ipv6":      {mutate: func(c *Config) { c.Metrics.Listen = "[::1]:9464" }},
		"metrics-no-port":   {mutate: func(c *Config) { c.Metrics.Listen = "localhost" }, errs: []string{`metrics.listen: must be host:port, got "localhost"`}},
		"metrics-bad-port":  {mutate: func(c *Config) { c.Metrics.Listen = "localhost:http" }, errs: []string{`metrics.listen: port must be a number from 0 to 65535, got "http"`}},
		"metrics-big-port":  {mutate: func(c *Config) { c.Metrics.Listen = "localhost:70000" }, errs: []string{`metrics.listen: port must be a number from 0 to 65535, got "70000"`}},
		"slack-missing-env": {mutate: func(c *Config) { c.Chat = Chat{"slack": {Type: ChatSlack}} }, errs: []string{"chat.slack.bot_token_env: required", "chat.slack.app_token_env: required"}},
		"slack-bad-env": {mutate: func(c *Config) { c.Chat = Chat{"slack": {Type: ChatSlack, BotTokenEnv: "1BOT", AppTokenEnv: "A-B"}} }, errs: []string{
			`chat.slack.bot_token_env: invalid environment variable name "1BOT"`,
			`chat.slack.app_token_env: invalid environment variable name "A-B"`,
		}},
		"chat-bad-type":     {mutate: func(c *Config) { c.Chat = Chat{"x": {Type: "irc"}} }, errs: []string{`chat.x.type: must be one of "slack", "discord", "stdio", "unix"; got "irc"`}},
		"unix":              {mutate: func(c *Config) { c.Chat = Chat{"ops": {Type: ChatUnix, Path: "/run/chatops.sock", Mode: "0600"}} }},
		"unix-missing-path": {mutate: func(c *Config) { c.Chat = Chat{"ops": {Type: ChatUnix}} }, errs: []string{"chat.ops.path: required for a unix backend"}},
		"unix-bad-mode":     {mutate: func(c *Config) { c.Chat = Chat{"ops": {Type: ChatUnix, Path: "/run/chatops.sock", Mode: "nope"}} }, errs: []string{`chat.ops.mode: must be octal permission bits (e.g. 0600), got "nope"`}},
		"chat-bad-name":     {mutate: func(c *Config) { c.Chat = Chat{"Bad_Name": {Type: ChatStdio}} }, errs: []string{`chat.Bad_Name: name must match ^[a-z][a-z0-9-]{0,31}$`}},
		"stdio-with-env":    {mutate: func(c *Config) { c.Chat = Chat{"console": {Type: ChatStdio, BotTokenEnv: "X"}} }, errs: []string{"chat.console: bot_token_env and app_token_env are not allowed for a stdio backend"}},
		"no-chat":           {mutate: func(c *Config) { c.Chat = nil }, errs: []string{"chat: at least one backend is required"}},
		"no-servers":        {mutate: func(c *Config) { c.MCP.Servers = nil }},
		"missing-base-url":  {mutate: func(c *Config) { c.LLM.BaseURL = "" }, errs: []string{"llm.base_url: required"}},
		"relative-base-url": {mutate: func(c *Config) { c.LLM.BaseURL = "localhost/v1" }, errs: []string{`llm.base_url: must be an absolute http or https URL, got "localhost/v1"`}},
		"ftp-base-url":      {mutate: func(c *Config) { c.LLM.BaseURL = "ftp://host/v1" }, errs: []string{`llm.base_url: must be an absolute http or https URL, got "ftp://host/v1"`}},
		"missing-model":     {mutate: func(c *Config) { c.LLM.Model = "" }, errs: []string{"llm.model: required"}},
		"bad-api-key-env":   {mutate: func(c *Config) { c.LLM.APIKeyEnv = "1KEY" }, errs: []string{`llm.api_key_env: invalid environment variable name "1KEY"`}},
		"good-api-key-env":  {mutate: func(c *Config) { c.LLM.APIKeyEnv = "LLM_API_KEY" }},
		"sampling-valid": {mutate: func(c *Config) {
			c.LLM.Sampling = Sampling{
				Temperature: pval(0.0), TopP: pval(1.0), TopK: pval(0), MinP: pval(0.0),
				MaxTokens: pval(256), FrequencyPenalty: pval(-2.0), PresencePenalty: pval(2.0),
				RepetitionPenalty: pval(1.0), Seed: pval(-5), Stop: []string{"END"},
			}
		}},
		"sampling-temperature-high": {mutate: func(c *Config) { c.LLM.Sampling.Temperature = pval(2.5) }, errs: []string{"llm.sampling.temperature: must be between 0 and 2, got 2.5"}},
		"sampling-top-p-low":        {mutate: func(c *Config) { c.LLM.Sampling.TopP = pval(-0.1) }, errs: []string{"llm.sampling.top_p: must be between 0 and 1, got -0.1"}},
		"sampling-min-p-high":       {mutate: func(c *Config) { c.LLM.Sampling.MinP = pval(1.5) }, errs: []string{"llm.sampling.min_p: must be between 0 and 1, got 1.5"}},
		"sampling-freq-penalty":     {mutate: func(c *Config) { c.LLM.Sampling.FrequencyPenalty = pval(3.0) }, errs: []string{"llm.sampling.frequency_penalty: must be between -2 and 2, got 3"}},
		"sampling-pres-penalty":     {mutate: func(c *Config) { c.LLM.Sampling.PresencePenalty = pval(-3.0) }, errs: []string{"llm.sampling.presence_penalty: must be between -2 and 2, got -3"}},
		"sampling-rep-penalty":      {mutate: func(c *Config) { c.LLM.Sampling.RepetitionPenalty = pval(0.0) }, errs: []string{"llm.sampling.repetition_penalty: must be positive"}},
		"sampling-top-k-negative":   {mutate: func(c *Config) { c.LLM.Sampling.TopK = pval(-1) }, errs: []string{"llm.sampling.top_k: must not be negative"}},
		"sampling-max-tokens-zero":  {mutate: func(c *Config) { c.LLM.Sampling.MaxTokens = pval(0) }, errs: []string{"llm.sampling.max_tokens: must be positive"}},
		"sampling-empty-stop":       {mutate: func(c *Config) { c.LLM.Sampling.Stop = []string{"ok", ""} }, errs: []string{"llm.sampling.stop: entry 1 is empty"}},
		"bad-server-id":             {mutate: func(c *Config) { c.MCP.Servers["Bad_ID"] = Server{Transport: TransportStdio, Command: "x"} }, errs: []string{`mcp.servers.Bad_ID: id must match ^[a-z][a-z0-9-]{0,31}$`}},
		"missing-transport":         {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Command: "x"} }, errs: []string{`mcp.servers.x.transport: must be "stdio" or "streamable-http", got ""`}},
		"unknown-transport":         {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Transport: "sse"} }, errs: []string{`mcp.servers.x.transport: must be "stdio" or "streamable-http", got "sse"`}},
		"stdio-no-command":          {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Transport: TransportStdio} }, errs: []string{"mcp.servers.x.command: required for stdio"}},
		"stdio-with-url":            {mutate: func(c *Config) { c.MCP.Servers["x"] = Server{Transport: TransportStdio, Command: "x", URL: "http://h"} }, errs: []string{"mcp.servers.x.url: not allowed for stdio"}},
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
				"agent.system_prompt: required",
				"agent.max_iterations: must be positive",
				"agent.turn_timeout: must be positive",
				"agent.tool_timeout: must be positive",
				"agent.max_tool_result_bytes: must be positive",
				"agent.history_ttl: must be positive",
				"agent.max_concurrent_turns: must be positive",
				"agent.max_pending_messages: must be positive",
				"agent.history_turns: must not be negative",
			},
		},
		"agent-empty-prompt":     {mutate: func(c *Config) { c.Agent.SystemPrompt = "   " }, errs: []string{"agent.system_prompt: required"}},
		"history-turns-zero":     {mutate: func(c *Config) { c.Agent.HistoryTurns = 0 }},
		"history-turns-positive": {mutate: func(c *Config) { c.Agent.HistoryTurns = 5 }},
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
