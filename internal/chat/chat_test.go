package chat

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
)

func Test_New(t *testing.T) {
	t.Setenv("CHATOPS_CHAT_TEST_BOT", "xoxb-1")
	t.Setenv("CHATOPS_CHAT_TEST_APP", "xapp-1")

	slackBackend, err := New("slack", config.ChatBackend{Type: config.ChatSlack, BotTokenEnv: "CHATOPS_CHAT_TEST_BOT", AppTokenEnv: "CHATOPS_CHAT_TEST_APP"}, nil)
	require.NoError(t, err)
	require.Contains(t, fmt.Sprintf("%T", slackBackend), "*slack.")

	stdioBackend, err := New("console", config.ChatBackend{Type: config.ChatStdio}, nil)
	require.NoError(t, err)
	require.Contains(t, fmt.Sprintf("%T", stdioBackend), "*stdio.")

	_, err = New("x", config.ChatBackend{Type: "irc"}, nil)
	require.EqualError(t, err, `chat backend "x": unknown type "irc"`)
}

func Test_Select(t *testing.T) {
	cfg := config.Chat{
		"console": {Type: config.ChatStdio},
		"other":   {Type: config.ChatStdio},
	}

	selected, err := Select(cfg, []string{"other", "console"}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"other", "console"}, []string{selected[0].Name, selected[1].Name})

	_, err = Select(cfg, []string{"ghost"}, nil)
	require.EqualError(t, err, `chat backend "ghost" is not defined in the config`)

	_, err = Select(config.Chat{"bad": {Type: "irc"}}, []string{"bad"}, nil)
	require.EqualError(t, err, `chat backend "bad": unknown type "irc"`)
}
