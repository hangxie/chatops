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
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, toMrkdwn(tc.in))
		})
	}
}
