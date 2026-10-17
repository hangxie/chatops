package slack

import (
	"regexp"

	"github.com/hangxie/chatops/internal/chat/markdown"
)

// Slack renders *bold* and bullets but not the GitHub-flavored Markdown the model tends to
// emit (**bold**, [text](url), # headings, tables), so those show up raw. These patterns convert the
// common cases; links become plain "text (url)" since replies are posted escaped and link-free.
var (
	mdBoldStars = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdBoldUnder = regexp.MustCompile(`__(.+?)__`)
	mdLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	mdHeading   = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.*?)[ \t]*$`)
	mdBullet    = regexp.MustCompile(`(?m)^([ \t]*)[-*][ \t]+`)
)

// toMrkdwn rewrites common GitHub-flavored Markdown into Slack mrkdwn, rendering tables as code blocks.
func toMrkdwn(text string) string {
	return markdown.Render(text, inlineMrkdwn)
}

func inlineMrkdwn(line string) string {
	line = mdBoldStars.ReplaceAllString(line, "*$1*")
	line = mdBoldUnder.ReplaceAllString(line, "*$1*")
	line = mdLink.ReplaceAllString(line, "$1 ($2)")
	line = mdHeading.ReplaceAllString(line, "*$1*")
	return mdBullet.ReplaceAllString(line, "$1• ")
}
