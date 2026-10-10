package slack

import (
	"strings"

	"github.com/slack-go/slack/slackevents"
)

// inbound is a user message addressed to the bot, with Slack's text escaping undone.
type inbound struct {
	team, channel, thread, ts, user, text string
}

// conversation keys history: one Slack thread is one conversation.
func (in inbound) conversation() string {
	return "slack:" + in.team + ":" + in.channel + ":" + in.thread
}

// id identifies the message itself, so retries and duplicate event types collapse.
func (in inbound) id() string {
	return in.team + ":" + in.channel + ":" + in.ts
}

var unescape = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// parseEvent accepts channel mentions that lead with the bot and any direct message.
func parseEvent(event slackevents.EventsAPIEvent, botUserID string) (inbound, bool) {
	if event.Type != slackevents.CallbackEvent {
		return inbound{}, false
	}
	var in inbound
	var botID, subtype string
	var direct bool
	switch inner := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		in = inbound{channel: inner.Channel, thread: inner.ThreadTimeStamp, ts: inner.TimeStamp, user: inner.User, text: inner.Text}
		botID = inner.BotID
	case *slackevents.MessageEvent:
		if inner.ChannelType != "im" {
			return inbound{}, false
		}
		in = inbound{channel: inner.Channel, thread: inner.ThreadTimeStamp, ts: inner.TimeStamp, user: inner.User, text: inner.Text}
		botID, subtype, direct = inner.BotID, inner.SubType, true
	default:
		return inbound{}, false
	}
	if in.channel == "" || in.ts == "" || in.user == "" || in.user == botUserID || botID != "" || subtype != "" {
		return inbound{}, false
	}

	text, mentioned := stripMention(in.text, botUserID)
	if !mentioned && !direct {
		return inbound{}, false
	}
	in.text = strings.TrimSpace(unescape.Replace(text))
	if in.text == "" {
		return inbound{}, false
	}
	in.team = event.TeamID
	if in.thread == "" {
		in.thread = in.ts
	}
	return in, true
}

// stripMention removes a leading "<@bot>" that is followed by whitespace or nothing.
func stripMention(text, botUserID string) (string, bool) {
	text = strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(text, "<@"+botUserID+">")
	if !ok || (rest != "" && !strings.ContainsAny(rest[:1], " \t\r\n")) {
		return text, false
	}
	return rest, true
}
