package markdown

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Render(t *testing.T) {
	upper := strings.ToUpper
	tests := map[string]struct {
		in     string
		inline func(string) string
		want   string
	}{
		"plain unchanged":       {in: "GitHub - All Systems Operational", want: "GitHub - All Systems Operational"},
		"empty":                 {in: "", want: ""},
		"inline rewrites prose": {in: "a\nb", inline: upper, want: "A\nB"},
		"table to aligned code block": {
			in:   "Deployments:\n\n| NAMESPACE | NAME | READY |\n|---|---|---|\n| airflow | wl-airflow-scheduler | 1/1 |\n| argocd   | argocd-server  | 2/2 |\n\nAll healthy.",
			want: "Deployments:\n\n```\nNAMESPACE  NAME                  READY\nairflow    wl-airflow-scheduler  1/1\nargocd     argocd-server         2/2\n```\n\nAll healthy.",
		},
		"table without outer pipes": {
			in:   "A | B\n:-- | --:\nx | yy",
			want: "```\nA  B\nx  yy\n```",
		},
		"table cell markup stripped": {
			in:   "| Name | State |\n| --- | --- |\n| **api** | `Healthy` |",
			want: "```\nName  State\napi   Healthy\n```",
		},
		"table cells skip inline": {
			in:     "| a | b |\n|---|---|\n| c | d |",
			inline: upper,
			want:   "```\na  b\nc  d\n```",
		},
		"short framed row ends table": {
			in:   "| Service | Status |\n|---|---|\n| API | OK |\n| This is an unrelated line |",
			want: "```\nService  Status\nAPI      OK\n```\n| This is an unrelated line |",
		},
		"prose with pipe ends framed table": {
			in:   "| Service | Status |\n|---|---|\n| API | OK |\nUse `grep foo | sort` to inspect logs.",
			want: "```\nService  Status\nAPI      OK\n```\nUse `grep foo | sort` to inspect logs.",
		},
		"column count ends unframed table": {
			in:   "A | B\n--|--\n1 | 2\nrun a | b | c",
			want: "```\nA  B\n1  2\n```\nrun a | b | c",
		},
		"pipe line without separator kept": {in: "a | b\nc | d", want: "a | b\nc | d"},
		"separator without header kept":    {in: "|---|---|", want: "|---|---|"},
		"fenced code kept verbatim": {
			in:     "```\n- raw | x\n|---|\n```\nitem",
			inline: upper,
			want:   "```\n- raw | x\n|---|\n```\nITEM",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, Render(tc.in, tc.inline))
		})
	}
}

func Test_Parse(t *testing.T) {
	segs := Parse("intro\n| a | b |\n|---|---|\n| 1 | 2 |\noutro", nil)
	require.Equal(t, []Segment{
		{Text: "intro"},
		{Table: [][]string{{"a", "b"}, {"1", "2"}}},
		{Text: "outro"},
	}, segs)
}

func Test_IsFence(t *testing.T) {
	tests := map[string]struct {
		line string
		want bool
	}{
		"bare fence":     {line: "```", want: true},
		"language fence": {line: "```yaml", want: true},
		"indented fence": {line: "  ```", want: true},
		"inline code":    {line: "use `x`", want: false},
		"empty":          {line: "", want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, IsFence(tc.line))
		})
	}
}

func Test_CodeBlock(t *testing.T) {
	tests := map[string]struct {
		rows [][]string
		want string
	}{
		"aligned":       {rows: [][]string{{"a", "bb"}, {"ccc", "d"}}, want: "```\na    bb\nccc  d\n```"},
		"ragged rows":   {rows: [][]string{{"a"}, {"b", "c"}}, want: "```\na\nb  c\n```"},
		"wide runes":    {rows: [][]string{{"é", "x"}, {"ab", "y"}}, want: "```\né   x\nab  y\n```"},
		"trailing trim": {rows: [][]string{{"a", ""}}, want: "```\na\n```"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, CodeBlock(tc.rows))
		})
	}
}
