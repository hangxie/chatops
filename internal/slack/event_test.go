package slack

import (
	"testing"

	"github.com/slack-go/slack/slackevents"
	"github.com/stretchr/testify/require"
)

const botUser = "UBOT"

func mention(mutate func(*slackevents.AppMentionEvent)) slackevents.EventsAPIEvent {
	inner := &slackevents.AppMentionEvent{User: "U1", Text: "<@UBOT> ping the server", TimeStamp: "1700000000.000100", Channel: "C1"}
	if mutate != nil {
		mutate(inner)
	}
	return callback(inner)
}

func direct(mutate func(*slackevents.MessageEvent)) slackevents.EventsAPIEvent {
	inner := &slackevents.MessageEvent{User: "U1", Text: "ping the server", TimeStamp: "1700000000.000200", Channel: "D1", ChannelType: "im"}
	if mutate != nil {
		mutate(inner)
	}
	return callback(inner)
}

func callback(inner any) slackevents.EventsAPIEvent {
	return slackevents.EventsAPIEvent{
		Type:       slackevents.CallbackEvent,
		TeamID:     "T1",
		InnerEvent: slackevents.EventsAPIInnerEvent{Data: inner},
	}
}

func Test_parseEvent(t *testing.T) {
	tests := map[string]struct {
		event slackevents.EventsAPIEvent
		want  *inbound
	}{
		"mention-root": {
			event: mention(nil),
			want:  &inbound{team: "T1", channel: "C1", thread: "1700000000.000100", ts: "1700000000.000100", user: "U1", text: "ping the server"},
		},
		"mention-in-thread": {
			event: mention(func(e *slackevents.AppMentionEvent) { e.ThreadTimeStamp = "1699999999.000001" }),
			want:  &inbound{team: "T1", channel: "C1", thread: "1699999999.000001", ts: "1700000000.000100", user: "U1", text: "ping the server"},
		},
		"mention-unescapes-text": {
			event: mention(func(e *slackevents.AppMentionEvent) { e.Text = "<@UBOT>\nis a &lt; b &amp;&amp; b &gt; c?" }),
			want:  &inbound{team: "T1", channel: "C1", thread: "1700000000.000100", ts: "1700000000.000100", user: "U1", text: "is a < b && b > c?"},
		},
		"mention-not-leading":   {event: mention(func(e *slackevents.AppMentionEvent) { e.Text = "ask <@UBOT> later" })},
		"mention-other-user":    {event: mention(func(e *slackevents.AppMentionEvent) { e.Text = "<@UOTHER> ping" })},
		"mention-no-space":      {event: mention(func(e *slackevents.AppMentionEvent) { e.Text = "<@UBOT>ping" })},
		"mention-empty-command": {event: mention(func(e *slackevents.AppMentionEvent) { e.Text = "<@UBOT>   " })},
		"mention-from-bot":      {event: mention(func(e *slackevents.AppMentionEvent) { e.BotID = "B1" })},
		"mention-from-self":     {event: mention(func(e *slackevents.AppMentionEvent) { e.User = botUser })},
		"mention-no-user":       {event: mention(func(e *slackevents.AppMentionEvent) { e.User = "" })},
		"mention-no-channel":    {event: mention(func(e *slackevents.AppMentionEvent) { e.Channel = "" })},
		"mention-no-ts":         {event: mention(func(e *slackevents.AppMentionEvent) { e.TimeStamp = "" })},
		"direct": {
			event: direct(nil),
			want:  &inbound{team: "T1", channel: "D1", thread: "1700000000.000200", ts: "1700000000.000200", user: "U1", text: "ping the server"},
		},
		"direct-with-mention": {
			event: direct(func(e *slackevents.MessageEvent) { e.Text = "<@UBOT> ping" }),
			want:  &inbound{team: "T1", channel: "D1", thread: "1700000000.000200", ts: "1700000000.000200", user: "U1", text: "ping"},
		},
		"direct-in-thread": {
			event: direct(func(e *slackevents.MessageEvent) { e.ThreadTimeStamp = "1699999999.000002" }),
			want:  &inbound{team: "T1", channel: "D1", thread: "1699999999.000002", ts: "1700000000.000200", user: "U1", text: "ping the server"},
		},
		"channel-message-ignored": {event: direct(func(e *slackevents.MessageEvent) { e.ChannelType = "channel" })},
		"direct-subtype":          {event: direct(func(e *slackevents.MessageEvent) { e.SubType = "message_changed" })},
		"direct-from-bot":         {event: direct(func(e *slackevents.MessageEvent) { e.BotID = "B1" })},
		"direct-from-self":        {event: direct(func(e *slackevents.MessageEvent) { e.User = botUser })},
		"direct-blank":            {event: direct(func(e *slackevents.MessageEvent) { e.Text = "  " })},
		"not-callback":            {event: slackevents.EventsAPIEvent{Type: slackevents.URLVerification}},
		"other-inner-event":       {event: callback(&slackevents.ReactionAddedEvent{User: "U1"})},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := parseEvent(tc.event, botUser)
			if tc.want == nil {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.Equal(t, *tc.want, got)
		})
	}
}

func Test_inbound_keys(t *testing.T) {
	in := inbound{team: "T1", channel: "C1", thread: "1.1", ts: "1.2"}
	require.Equal(t, "slack:T1:C1:1.1", in.conversation())
	require.Equal(t, "T1:C1:1.2", in.id())
}
