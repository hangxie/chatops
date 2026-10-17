package discord

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/conversation"
)

func Test_messages(t *testing.T) {
	long := strings.Repeat("x", 1500)
	tests := map[string]struct {
		reply conversation.Reply
		want  []string
	}{
		"empty":      {reply: conversation.Reply{}, want: nil},
		"plain text": {reply: conversation.Reply{Text: "**up** [page](https://x.io)"}, want: []string{"**up** [page](https://x.io)"}},
		"table to code block": {
			reply: conversation.Reply{Text: "| a | b |\n|---|---|\n| 1 | 2 |"},
			want:  []string{"```\na  b\n1  2\n```"},
		},
		"footer as subtext": {
			reply: conversation.Reply{Text: "ok", Footer: "plan 1s\ntotal 2s"},
			want:  []string{"ok\n\n-# plan 1s\n-# total 2s"},
		},
		"footer only": {
			reply: conversation.Reply{Footer: "total 2s"},
			want:  []string{"-# total 2s"},
		},
		"footer in its own message when no room": {
			reply: conversation.Reply{Text: long, Footer: strings.Repeat("f", 600)},
			want:  []string{long, "-# " + strings.Repeat("f", 600)},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, messages(tc.reply))
		})
	}
}

func Test_messages_truncated(t *testing.T) {
	line := strings.Repeat("y", 999)
	text := strings.TrimSuffix(strings.Repeat(line+"\n", 20), "\n")
	got := messages(conversation.Reply{Text: text, Footer: "total 1s"})
	require.Len(t, got, maxMessages)
	require.True(t, strings.HasSuffix(got[maxMessages-1], truncatedNote+"\n\n-# total 1s"))
	for _, m := range got {
		require.LessOrEqual(t, utf8.RuneCountInString(m), maxMessageRunes)
	}
}

func Test_subtext(t *testing.T) {
	tests := map[string]struct {
		in, want string
	}{
		"empty":       {in: "", want: ""},
		"blank lines": {in: "\n a \n\n b\n", want: "-# a\n-# b"},
		"too long":    {in: strings.Repeat("z", 2000), want: "-# " + strings.Repeat("z", maxFooterRunes-4) + "…"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := subtext(tc.in)
			require.Equal(t, tc.want, got)
			require.LessOrEqual(t, utf8.RuneCountInString(got), maxFooterRunes)
		})
	}
}

func Test_chunk(t *testing.T) {
	tests := map[string]struct {
		text  string
		limit int
		want  []string
	}{
		"fits":          {text: "a\nb", limit: 20, want: []string{"a\nb"}},
		"blank dropped": {text: "\n\n", limit: 20, want: nil},
		"split at line": {text: "aaaaaaaa\nbbbbbbbb\ncccccccc", limit: 20, want: []string{"aaaaaaaa\nbbbbbbbb", "cccccccc"}},
		"long line cut": {text: strings.Repeat("a", 25), limit: 17, want: []string{strings.Repeat("a", 17), strings.Repeat("a", 8)}},
		"long fenced line cut": {
			text:  "```\n" + strings.Repeat("a", 20) + "\n```",
			limit: 20,
			want:  []string{"```\naaaaaaaaaaaa\n```", "```\naaaaaaaa\n```"},
		},
		"language fence reopened": {
			text:  "```go\nfunc a()\nfunc b()\n```",
			limit: 22,
			want:  []string{"```go\nfunc a()\n```", "```go\nfunc b()\n```"},
		},
		"long opener reopened bare": {
			text:  "```" + strings.Repeat("x", 20) + "\n1111111\n2222222\n```",
			limit: 40,
			want:  []string{"```" + strings.Repeat("x", 20) + "\n1111111\n```", "```\n2222222\n```"},
		},
		"fence closed and reopened": {
			text:  "```\n1111\n2222\n3333\n```",
			limit: 18,
			want:  []string{"```\n1111\n2222\n```", "```\n3333\n```"},
		},
		"unclosed fence closed": {text: "```\nabc", limit: 20, want: []string{"```\nabc\n```"}},
		"multibyte runes":       {text: "éééééééé\néééééééé", limit: 17, want: []string{"éééééééé\néééééééé"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := chunk(tc.text, tc.limit)
			require.Equal(t, tc.want, got)
			for _, c := range got {
				require.LessOrEqual(t, utf8.RuneCountInString(c), tc.limit)
			}
		})
	}
}

func Test_splitRunes(t *testing.T) {
	require.Equal(t, []string{"ab"}, splitRunes("ab", 2))
	require.Equal(t, []string{"ab", "c"}, splitRunes("abc", 2))
	require.Equal(t, []string{"éé", "é"}, splitRunes("ééé", 2))
	require.Equal(t, []string{""}, splitRunes("", 2))
}

// requireDiscordMessages checks every message fits Discord's limit and closes each code fence it opens.
func requireDiscordMessages(t *testing.T, got []string) {
	t.Helper()
	for i, m := range got {
		require.LessOrEqual(t, utf8.RuneCountInString(m), maxMessageRunes, "message %d", i)
		fences := 0
		for line := range strings.SplitSeq(m, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fences++
			}
		}
		require.Zero(t, fences%2, "message %d has an unclosed fence", i)
	}
}

func Test_messages_at_discord_limit(t *testing.T) {
	var code strings.Builder
	code.WriteString("```go\n")
	for i := range 150 {
		fmt.Fprintf(&code, "fmt.Println(\"line %03d\")\n", i)
	}
	code.WriteString("```")

	table := "| NAMESPACE | NAME | READY |\n|---|---|---|\n"
	for i := range 120 {
		table += fmt.Sprintf("| ns-%03d | deployment-%03d | 1/1 |\n", i, i)
	}

	tests := map[string]struct {
		text      string
		wantCount int
		check     func(t *testing.T, got []string)
	}{
		"language fence across messages": {
			text: code.String(), wantCount: 2,
			check: func(t *testing.T, got []string) {
				for _, m := range got {
					require.True(t, strings.HasPrefix(m, "```go\n"))
					require.True(t, strings.HasSuffix(m, "\n```"))
				}
			},
		},
		"long line inside fence": {
			text: "```\n" + strings.Repeat("x", 5000) + "\n```", wantCount: 3,
			check: func(t *testing.T, got []string) {
				var body strings.Builder
				for _, m := range got {
					body.WriteString(strings.ReplaceAll(strings.Trim(m, "`\n"), "\n", ""))
				}
				require.Equal(t, strings.Repeat("x", 5000), body.String())
			},
		},
		"large table": {
			text: table, wantCount: 2,
			check: func(t *testing.T, got []string) {
				require.True(t, strings.HasPrefix(got[0], "```\nNAMESPACE  NAME"))
				require.Contains(t, got[1], "ns-119")
			},
		},
		"multibyte exactly at limit": {
			text: strings.Repeat("é", maxMessageRunes-utf8.RuneCountInString(truncatedNote)), wantCount: 1,
		},
		"multibyte one past limit": {
			text: strings.Repeat("é", maxMessageRunes-utf8.RuneCountInString(truncatedNote)+1), wantCount: 2,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := messages(conversation.Reply{Text: tc.text})
			require.Len(t, got, tc.wantCount)
			requireDiscordMessages(t, got)
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}
