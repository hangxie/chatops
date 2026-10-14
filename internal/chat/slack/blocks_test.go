package slack

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	slackapi "github.com/slack-go/slack"
	"github.com/stretchr/testify/require"
)

const blocksTable = "| NAME | READY |\n|---|---|\n| **api** | 1/1 |"

func Test_toBlocks(t *testing.T) {
	tableJSON := `{"type":"table","rows":[[{"type":"raw_text","text":"NAME"},{"type":"raw_text","text":"READY"}],[{"type":"raw_text","text":"api"},{"type":"raw_text","text":"1/1"}]]}`
	section := func(text string) string {
		b, err := json.Marshal(text)
		require.NoError(t, err)
		return `{"type":"section","text":{"type":"mrkdwn","text":` + string(b) + `}}`
	}
	manyRows := "| A |\n|---|" + strings.Repeat("\n| x |", maxTableRows)
	tests := map[string]struct {
		in   string
		want string
	}{
		"no table":                    {in: "**ok** <!here>", want: "null"},
		"table only":                  {in: blocksTable, want: "[" + tableJSON + "]"},
		"prose around table":          {in: "**Deployments** <x>:\n" + blocksTable + "\n\n- a & b", want: "[" + section("*Deployments* &lt;x&gt;:") + "," + tableJSON + "," + section("• a &amp; b") + "]"},
		"second table code block":     {in: blocksTable + "\nnext:\n" + "| A |\n|---|\n| 1 |", want: "[" + tableJSON + "," + section("next:\n```\nA\n1\n```") + "]"},
		"prose with pipe after table": {in: blocksTable + "\nUse `grep foo | sort` here.", want: "[" + tableJSON + "," + section("Use `grep foo | sort` here.") + "]"},
		"too many rows":               {in: manyRows, want: "null"},
		"malformed row after table":   {in: blocksTable + "\n| unrelated |", want: "[" + tableJSON + "," + section("| unrelated |") + "]"},
		"empty cell":                  {in: "| A | B |\n|---|---|\n| 1 |  |", want: "null"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := json.Marshal(toBlocks(tc.in))
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(got))
		})
	}
}

func Test_toBlocks_caps_block_count(t *testing.T) {
	long := strings.Repeat(strings.Repeat("x", maxSectionChars)+"\n", maxBlocks+5)
	blocks := toBlocks(long + blocksTable)
	require.Len(t, blocks, maxBlocks)
	require.Equal(t, "table", string(blocks[maxBlocks-2].BlockType()), "the table survives the cap")
	last, err := json.Marshal(blocks[maxBlocks-1])
	require.NoError(t, err)
	require.Contains(t, string(last), "[reply truncated]")
}

func Test_toBlocks_keeps_long_code_fenced(t *testing.T) {
	code := "```\n" + strings.Repeat("log line\n", maxSectionChars/5) + "```"
	blocks := toBlocks(blocksTable + "\n" + code)
	require.Greater(t, len(blocks), 2, "the code spans several sections")
	for _, b := range blocks[1:] {
		text := b.(*slackapi.SectionBlock).Text.Text
		require.LessOrEqual(t, utf8.RuneCountInString(text), maxSectionChars)
		require.True(t, strings.HasPrefix(text, "```\n") && strings.HasSuffix(text, "\n```"), "each section is a whole code block")
	}
}

func Test_capBlocks(t *testing.T) {
	section := slackapi.NewSectionBlock(slackapi.NewTextBlockObject(slackapi.MarkdownType, "s", false, false), nil, nil)
	table := tableBlock([][]string{{"A"}})
	tests := map[string]struct {
		in   []slackapi.Block
		want []string
	}{
		"under cap":          {in: []slackapi.Block{section, table}, want: []string{"section", "table"}},
		"at cap":             {in: []slackapi.Block{section, section, table}, want: []string{"section", "section", "table"}},
		"over cap":           {in: []slackapi.Block{section, section, section, section}, want: []string{"section", "section", "section"}},
		"table kept":         {in: []slackapi.Block{table, section, section, section}, want: []string{"table", "section", "section"}},
		"table moved inside": {in: []slackapi.Block{section, section, section, table}, want: []string{"section", "table", "section"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := capBlocks(tc.in, 3)
			types := make([]string, len(got))
			for i, b := range got {
				types[i] = string(b.BlockType())
			}
			require.Equal(t, tc.want, types)
		})
	}
}

func Test_nativeTable(t *testing.T) {
	wide := make([]string, maxTableCols+1)
	for i := range wide {
		wide[i] = "x"
	}
	tests := map[string]struct {
		rows [][]string
		want bool
	}{
		"fits":           {rows: [][]string{{"A", "B"}, {"1", "2"}}, want: true},
		"too many cols":  {rows: [][]string{wide}, want: false},
		"too many chars": {rows: [][]string{{strings.Repeat("x", maxTableChars)}, {"y"}}, want: false},
		"ragged":         {rows: [][]string{{"A", "B"}, {"1"}}, want: false},
		"empty cell":     {rows: [][]string{{"A"}, {""}}, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, nativeTable(tc.rows))
		})
	}
}

func Test_chunkEscaped(t *testing.T) {
	tests := map[string]struct {
		in    string
		limit int
		want  []string
	}{
		"blank skipped":             {in: "\n \n", limit: 10, want: nil},
		"fits":                      {in: "\na & b\n", limit: 10, want: []string{"a &amp; b"}},
		"splits at lines":           {in: "aaaa\nbbbb\ncc", limit: 9, want: []string{"aaaa\nbbbb", "cc"}},
		"splits long line":          {in: "abcdefg", limit: 3, want: []string{"abc", "def", "g"}},
		"keeps entity intact":       {in: "a&b", limit: 5, want: []string{"a", "&amp;", "b"}},
		"fence closed and reopened": {in: "```\naaaa\nbbbb\n```", limit: 13, want: []string{"```\naaaa\n```", "```\nbbbb\n```"}},
		"long line in fence":        {in: "```\nabcdefghij\n```", limit: 13, want: []string{"```\nabcde\n```", "```\nfghij\n```"}},
		"fence starts new chunk":    {in: "intro\n```\naa\n```", limit: 12, want: []string{"intro", "```\naa\n```"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, chunkEscaped(tc.in, tc.limit))
		})
	}
}
