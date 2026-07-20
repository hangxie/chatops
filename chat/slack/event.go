package slack

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/hangxie/chatops/chat"
)

const choiceActionIDPrefix = "chatops.choice."

type choiceInteraction struct {
	channel          string
	messageTimestamp string
	displayText      string
	message          chat.Message
}

func messageFromEvent(event socketmode.Event, botUserID string) (chat.Message, conversation, bool) {
	if event.Type != socketmode.EventTypeEventsAPI {
		return chat.Message{}, conversation{}, false
	}
	outer, ok := event.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return chat.Message{}, conversation{}, false
	}
	var channel, user, text, timestamp, thread, botID, subtype string
	switch inner := outer.InnerEvent.Data.(type) {
	case *slackevents.MessageEvent:
		channel, user, text = inner.Channel, inner.User, inner.Text
		timestamp, thread = inner.TimeStamp, inner.ThreadTimeStamp
		botID, subtype = inner.BotID, inner.SubType
	case *slackevents.AppMentionEvent:
		channel, user, text = inner.Channel, inner.User, inner.Text
		timestamp, thread, botID = inner.TimeStamp, inner.ThreadTimeStamp, inner.BotID
	default:
		return chat.Message{}, conversation{}, false
	}
	if channel == "" || user == "" || text == "" || timestamp == "" || botID != "" || subtype != "" {
		return chat.Message{}, conversation{}, false
	}
	text, ok = stripRecipientMention(text, botUserID)
	if !ok {
		return chat.Message{}, conversation{}, false
	}
	sentAt, err := parseTimestamp(timestamp)
	if err != nil {
		return chat.Message{}, conversation{}, false
	}
	if thread == "" {
		thread = timestamp
	}
	return chat.Message{
		ConversationID: conversationID(channel, thread),
		Sender:         user,
		Text:           text,
		Timestamp:      sentAt,
	}, conversation{channel: channel, thread: thread}, true
}

func choiceFromEvent(event socketmode.Event) (choiceInteraction, bool) {
	if event.Type != socketmode.EventTypeInteractive {
		return choiceInteraction{}, false
	}
	callback, ok := event.Data.(slackapi.InteractionCallback)
	if !ok || callback.Type != slackapi.InteractionTypeBlockActions ||
		callback.User.ID == "" || len(callback.ActionCallback.BlockActions) != 1 {
		return choiceInteraction{}, false
	}
	action := callback.ActionCallback.BlockActions[0]
	if action == nil || !isChoiceActionID(action.ActionID) || action.Value == "" {
		return choiceInteraction{}, false
	}
	channel := callback.Container.ChannelID
	if channel == "" {
		channel = callback.Channel.ID
	}
	messageTimestamp := callback.Container.MessageTs
	if messageTimestamp == "" {
		messageTimestamp = callback.Message.Timestamp
	}
	if channel == "" || messageTimestamp == "" {
		return choiceInteraction{}, false
	}
	sentAt, err := parseTimestamp(action.ActionTs)
	if err != nil {
		return choiceInteraction{}, false
	}
	displayText := callback.Message.Text
	if displayText == "" {
		displayText = action.Value
	}
	return choiceInteraction{
		channel:          channel,
		messageTimestamp: messageTimestamp,
		displayText:      displayText,
		message: chat.Message{
			Sender:    callback.User.ID,
			Text:      action.Value,
			Timestamp: sentAt,
		},
	}, true
}

func choiceActionID(index int) string {
	return choiceActionIDPrefix + strconv.Itoa(index)
}

func isChoiceActionID(actionID string) bool {
	suffix := strings.TrimPrefix(actionID, choiceActionIDPrefix)
	index, err := strconv.ParseUint(suffix, 10, 64)
	return err == nil && strconv.FormatUint(index, 10) == suffix
}

func stripRecipientMention(text, botUserID string) (string, bool) {
	text = strings.TrimSpace(text)
	if botUserID == "" {
		return "", false
	}
	mention := "<@" + botUserID + ">"
	if !strings.HasPrefix(text, mention) {
		return "", false
	}
	return commandAfterMention(text[len(mention):])
}

func commandAfterMention(remainder string) (string, bool) {
	if remainder == "" || !strings.ContainsRune(" \t\r\n", rune(remainder[0])) {
		return "", false
	}
	command := strings.TrimSpace(remainder)
	return command, command != ""
}

func conversationID(channel, thread string) string { return "slack:" + channel + ":" + thread }

func parseTimestamp(value string) (time.Time, error) {
	parts := strings.SplitN(value, ".", 2)
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid Slack timestamp %q: %w", value, err)
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	fraction = (fraction + "000000000")[:9]
	nanoseconds, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid Slack timestamp %q: %w", value, err)
	}
	return time.Unix(seconds, nanoseconds), nil
}
