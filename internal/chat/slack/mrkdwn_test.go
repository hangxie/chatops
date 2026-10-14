package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_toMrkdwn(t *testing.T) {
	tests := map[string]struct {
		in   string
		want string
	}{
		"plain unchanged":      {in: "GitHub - All Systems Operational", want: "GitHub - All Systems Operational"},
		"bold stars":           {in: "status is **down**", want: "status is *down*"},
		"bold underscores":     {in: "status is __down__", want: "status is *down*"},
		"single star kept":     {in: "already *bold*", want: "already *bold*"},
		"link to text and url": {in: "see [status page](https://x.io/s)", want: "see status page (https://x.io/s)"},
		"heading to bold":      {in: "# Services\nok", want: "*Services*\nok"},
		"subheading to bold":   {in: "### Sub", want: "*Sub*"},
		"dash bullet":          {in: "- item", want: "• item"},
		"star bullet":          {in: "* item", want: "• item"},
		"indented bullet":      {in: "  - nested", want: "  • nested"},
		"horizontal rule kept": {in: "---", want: "---"},
		"all-list line":        {in: "- **GitHub**: All Systems Operational", want: "• *GitHub*: All Systems Operational"},
		"multiline all-list":   {in: "- **GitHub**: OK\n- **Slack**: Active Incident", want: "• *GitHub*: OK\n• *Slack*: Active Incident"},
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
			in:   "```\n- **raw** | x\n|---|\n```\n- item",
			want: "```\n- **raw** | x\n|---|\n```\n• item",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, toMrkdwn(tc.in))
		})
	}
}
