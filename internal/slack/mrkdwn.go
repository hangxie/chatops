package slack

import "regexp"

// Slack renders *bold* and bullets but not the GitHub-flavored Markdown the model tends to
// emit (**bold**, [text](url), # headings), so those show up raw. These patterns convert the
// common cases; links become plain "text (url)" since replies are posted escaped and link-free.
var (
	mdBoldStars = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdBoldUnder = regexp.MustCompile(`__(.+?)__`)
	mdLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	mdHeading   = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.*?)[ \t]*$`)
	mdBullet    = regexp.MustCompile(`(?m)^([ \t]*)[-*][ \t]+`)
)

// toMrkdwn rewrites common GitHub-flavored Markdown into Slack mrkdwn.
func toMrkdwn(text string) string {
	text = mdBoldStars.ReplaceAllString(text, "*$1*")
	text = mdBoldUnder.ReplaceAllString(text, "*$1*")
	text = mdLink.ReplaceAllString(text, "$1 ($2)")
	text = mdHeading.ReplaceAllString(text, "*$1*")
	text = mdBullet.ReplaceAllString(text, "$1• ")
	return text
}
