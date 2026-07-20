package slack

import (
	"context"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"
)

func messageEvent(envelopeID string, event *slackevents.MessageEvent) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: event},
		},
		Request: &socketmode.Request{EnvelopeID: envelopeID},
	}
}

func Test_Receive_maps_root_and_thread_messages(t *testing.T) {
	testCases := map[string]struct {
		event        *slackevents.MessageEvent
		conversation string
		command      string
		timestamp    time.Time
	}{
		"root": {
			event:        &slackevents.MessageEvent{Channel: "C123", User: "U123", Text: "<@UCHATOPS> deploy", TimeStamp: "1720000000.123456"},
			conversation: "slack:C123:1720000000.123456",
			command:      "deploy",
			timestamp:    time.Unix(1720000000, 123456000),
		},
		"thread-reply": {
			event:        &slackevents.MessageEvent{Channel: "C123", User: "U456", Text: "<@UCHATOPS> yes", TimeStamp: "1720000001.000001", ThreadTimeStamp: "1720000000.123456"},
			conversation: "slack:C123:1720000000.123456",
			command:      "yes",
			timestamp:    time.Unix(1720000001, 1000),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			conn, socket, _ := testConn(t)
			socket.events <- messageEvent("E1", tc.event)
			msg, err := conn.Receive(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.conversation, msg.ConversationID)
			require.Equal(t, tc.event.User, msg.Sender)
			require.Equal(t, tc.command, msg.Text)
			require.Equal(t, tc.timestamp, msg.Timestamp)
			require.Eventually(t, func() bool { return len(socket.ackedIDs()) == 1 }, time.Second, time.Millisecond)
			require.Equal(t, []string{"E1"}, socket.ackedIDs())
		})
	}
}

func Test_Receive_ignores_non_user_messages(t *testing.T) {
	conn, socket, _ := testConn(t)
	ignored := []*slackevents.MessageEvent{
		{Channel: "C1", User: "U1", Text: "edited", TimeStamp: "1.1", SubType: "message_changed"},
		{Channel: "C1", User: "U1", Text: "bot", TimeStamp: "1.2", BotID: "B1"},
		{Channel: "C1", Text: "no sender", TimeStamp: "1.3"},
		{Channel: "C1", User: "U1", TimeStamp: "1.4"},
		{Channel: "C1", User: "U1", Text: "no mention", TimeStamp: "1.5"},
	}
	for i, event := range ignored {
		socket.events <- messageEvent(string(rune('A'+i)), event)
	}
	socket.events <- messageEvent("E", &slackevents.MessageEvent{Channel: "C1", User: "U1", Text: "<@UCHATOPS> real", TimeStamp: "2.1"})
	msg, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, "real", msg.Text)
	require.Eventually(t, func() bool { return len(socket.ackedIDs()) == 6 }, time.Second, time.Millisecond)
}

func Test_messageFromEvent_app_mention(t *testing.T) {
	event := socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{
			Channel: "C1", User: "U1", Text: "<@UCHATOPS> ping", TimeStamp: "10.2", ThreadTimeStamp: "10.1",
		}}},
	}
	msg, route, ok := messageFromEvent(event, "UCHATOPS")
	require.True(t, ok)
	require.Equal(t, "slack:C1:10.1", msg.ConversationID)
	require.Equal(t, "ping", msg.Text)
	require.Equal(t, conversation{channel: "C1", thread: "10.1"}, route)
}

func Test_messageFromEvent_rejects_app_mention_with_other_leading_user(t *testing.T) {
	event := socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{
			Channel: "C1", User: "U1", Text: "<@UALICE> deploy <@UCHATOPS>", TimeStamp: "10.2",
		}}},
	}
	_, _, ok := messageFromEvent(event, "UCHATOPS")
	require.False(t, ok)
}

func Test_messageFromEvent_ignores_unrecognized_events(t *testing.T) {
	testCases := map[string]socketmode.Event{
		"socket-event": {Type: socketmode.EventTypeConnected},
		"wrong-data":   {Type: socketmode.EventTypeEventsAPI, Data: "bad"},
		"wrong-inner": {
			Type: socketmode.EventTypeEventsAPI,
			Data: slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{Data: struct{}{}}},
		},
	}
	for name, event := range testCases {
		t.Run(name, func(t *testing.T) {
			_, _, ok := messageFromEvent(event, "UCHATOPS")
			require.False(t, ok)
		})
	}
}

func Test_stripRecipientMention(t *testing.T) {
	testCases := map[string]struct {
		text    string
		botID   string
		command string
		ok      bool
	}{
		"bot":          {text: "<@UCHATOPS> ping", botID: "UCHATOPS", command: "ping", ok: true},
		"other-user":   {text: "<@UALICE> deploy", botID: "UCHATOPS"},
		"bot-later":    {text: "hello <@UCHATOPS> deploy", botID: "UCHATOPS"},
		"no-recipient": {text: "ping", botID: "UCHATOPS"},
		"no-command":   {text: "<@UCHATOPS>", botID: "UCHATOPS"},
		"no-separator": {text: "<@UCHATOPS>ping", botID: "UCHATOPS"},
		"empty-bot-id": {text: "<@UCHATOPS> ping"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			command, ok := stripRecipientMention(tc.text, tc.botID)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.command, command)
		})
	}
}

func Test_parseTimestamp_invalid(t *testing.T) {
	testCases := []string{"", "invalid", "1.invalid"}
	for _, value := range testCases {
		t.Run(value, func(t *testing.T) {
			_, err := parseTimestamp(value)
			require.Error(t, err)
		})
	}
}

func Test_messageFromEvent_rejects_malformed_timestamp(t *testing.T) {
	event := messageEvent("E1", &slackevents.MessageEvent{
		Channel: "C1", User: "U1", Text: "<@UCHATOPS> ping", TimeStamp: "not-a-timestamp",
	})
	_, _, ok := messageFromEvent(event, "UCHATOPS")
	require.False(t, ok)
}

func Test_choiceFromEvent(t *testing.T) {
	event := socketmode.Event{Type: socketmode.EventTypeInteractive, Data: slackapi.InteractionCallback{
		Type:      slackapi.InteractionTypeBlockActions,
		Channel:   slackapi.Channel{GroupConversation: slackapi.GroupConversation{Conversation: slackapi.Conversation{ID: "C1"}}},
		User:      slackapi.User{ID: "U1"},
		Message:   slackapi.Message{Msg: slackapi.Msg{Timestamp: "20.1"}},
		Container: slackapi.Container{MessageTs: "20.1", ChannelID: "C1"},
		ActionCallback: slackapi.ActionCallbacks{BlockActions: []*slackapi.BlockAction{{
			ActionID: choiceActionID(0), Value: "yes", ActionTs: "1720000001.000001",
		}}},
	}}

	choice, ok := choiceFromEvent(event)
	require.True(t, ok)
	require.Equal(t, "C1", choice.channel)
	require.Equal(t, "20.1", choice.messageTimestamp)
	require.Equal(t, "U1", choice.message.Sender)
	require.Equal(t, "yes", choice.message.Text)
	require.Equal(t, time.Unix(1720000001, 1000), choice.message.Timestamp)
}

func Test_choiceFromEvent_uses_message_address_fallback(t *testing.T) {
	event := socketmode.Event{Type: socketmode.EventTypeInteractive, Data: slackapi.InteractionCallback{
		Type:    slackapi.InteractionTypeBlockActions,
		Channel: slackapi.Channel{GroupConversation: slackapi.GroupConversation{Conversation: slackapi.Conversation{ID: "C1"}}},
		User:    slackapi.User{ID: "U1"},
		Message: slackapi.Message{Msg: slackapi.Msg{Timestamp: "20.1", Text: "continue?"}},
		ActionCallback: slackapi.ActionCallbacks{BlockActions: []*slackapi.BlockAction{{
			ActionID: choiceActionID(0), Value: "yes", ActionTs: "30.1",
		}}},
	}}

	choice, ok := choiceFromEvent(event)
	require.True(t, ok)
	require.Equal(t, "C1", choice.channel)
	require.Equal(t, "20.1", choice.messageTimestamp)
	require.Equal(t, "continue?", choice.displayText)
}

func Test_choiceFromEvent_rejects_missing_address_and_bad_timestamp(t *testing.T) {
	callback := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeBlockActions,
		User: slackapi.User{ID: "U1"},
		ActionCallback: slackapi.ActionCallbacks{BlockActions: []*slackapi.BlockAction{{
			ActionID: choiceActionID(0), Value: "yes", ActionTs: "30.1",
		}}},
	}
	_, ok := choiceFromEvent(socketmode.Event{Type: socketmode.EventTypeInteractive, Data: callback})
	require.False(t, ok)

	callback.Container = slackapi.Container{ChannelID: "C1", MessageTs: "20.1"}
	callback.ActionCallback.BlockActions[0].ActionTs = "bad"
	_, ok = choiceFromEvent(socketmode.Event{Type: socketmode.EventTypeInteractive, Data: callback})
	require.False(t, ok)
}

func Test_choiceFromEvent_rejects_invalid_payload(t *testing.T) {
	valid := slackapi.InteractionCallback{
		Type:    slackapi.InteractionTypeBlockActions,
		Channel: slackapi.Channel{GroupConversation: slackapi.GroupConversation{Conversation: slackapi.Conversation{ID: "C1"}}},
		User:    slackapi.User{ID: "U1"},
		Message: slackapi.Message{Msg: slackapi.Msg{Timestamp: "20.1"}},
		ActionCallback: slackapi.ActionCallbacks{BlockActions: []*slackapi.BlockAction{{
			ActionID: choiceActionID(0), Value: "yes", ActionTs: "1.1",
		}}},
	}
	testCases := map[string]socketmode.Event{
		"wrong-event": {Type: socketmode.EventTypeConnected},
		"wrong-data":  {Type: socketmode.EventTypeInteractive, Data: "bad"},
		"wrong-type": {Type: socketmode.EventTypeInteractive, Data: func() slackapi.InteractionCallback {
			v := valid
			v.Type = slackapi.InteractionTypeViewSubmission
			return v
		}()},
		"wrong-action": {Type: socketmode.EventTypeInteractive, Data: func() slackapi.InteractionCallback {
			v := valid
			v.ActionCallback.BlockActions[0].ActionID = "foreign"
			return v
		}()},
		"missing-user": {Type: socketmode.EventTypeInteractive, Data: func() slackapi.InteractionCallback { v := valid; v.User.ID = ""; return v }()},
	}
	for name, event := range testCases {
		t.Run(name, func(t *testing.T) {
			_, ok := choiceFromEvent(event)
			require.False(t, ok)
		})
	}
}

func Test_choiceActionID(t *testing.T) {
	testCases := map[string]bool{
		choiceActionID(0):   true,
		choiceActionID(12):  true,
		"chatops.choice":    false,
		"chatops.choice.":   false,
		"chatops.choice.-1": false,
		"chatops.choice.01": false,
		"foreign.0":         false,
	}
	for actionID, want := range testCases {
		t.Run(actionID, func(t *testing.T) {
			require.Equal(t, want, isChoiceActionID(actionID))
		})
	}
}
