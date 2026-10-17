package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"
)

func message(guild, content string) *discordgo.Message {
	return &discordgo.Message{
		ID: "M1", ChannelID: "C1", GuildID: guild, Content: content,
		Author: &discordgo.User{ID: "U1"}, Type: discordgo.MessageTypeDefault,
	}
}

func Test_parseMessage(t *testing.T) {
	threads := func(id string) bool { return id == "T1" }
	tests := map[string]struct {
		msg    *discordgo.Message
		botID  string
		want   inbound
		wantOK bool
	}{
		"channel mention": {
			msg: message("G1", "<@BOT> is it up?"), botID: "BOT", wantOK: true,
			want: inbound{guild: "G1", channel: "C1", message: "M1", user: "U1", text: "is it up?"},
		},
		"nickname mention": {
			msg: message("G1", "<@!BOT>\nstatus"), botID: "BOT", wantOK: true,
			want: inbound{guild: "G1", channel: "C1", message: "M1", user: "U1", text: "status"},
		},
		"direct message without mention": {
			msg: message("", "status"), botID: "BOT", wantOK: true,
			want: inbound{channel: "C1", message: "M1", user: "U1", text: "status"},
		},
		"direct message with mention": {
			msg: message("", "<@BOT> status"), botID: "BOT", wantOK: true,
			want: inbound{channel: "C1", message: "M1", user: "U1", text: "status"},
		},
		"thread mention": {
			msg:   func() *discordgo.Message { m := message("G1", "<@BOT> again"); m.ChannelID = "T1"; return m }(),
			botID: "BOT", wantOK: true,
			want: inbound{guild: "G1", channel: "T1", message: "M1", user: "U1", text: "again", thread: true},
		},
		"reply type accepted": {
			msg: func() *discordgo.Message {
				m := message("G1", "<@BOT> x")
				m.Type = discordgo.MessageTypeReply
				return m
			}(),
			botID: "BOT", wantOK: true,
			want: inbound{guild: "G1", channel: "C1", message: "M1", user: "U1", text: "x"},
		},
		"channel without mention": {msg: message("G1", "status"), botID: "BOT"},
		"mention not leading":     {msg: message("G1", "hey <@BOT> status"), botID: "BOT"},
		"mention glued to text":   {msg: message("G1", "<@BOT>status"), botID: "BOT"},
		"other user mentioned":    {msg: message("G1", "<@OTHER> status"), botID: "BOT"},
		"mention only":            {msg: message("G1", "<@BOT>  "), botID: "BOT"},
		"empty direct message":    {msg: message("", " "), botID: "BOT"},
		"bot not identified yet":  {msg: message("", "status")},
		"nil message":             {botID: "BOT"},
		"missing author":          {msg: func() *discordgo.Message { m := message("", "x"); m.Author = nil; return m }(), botID: "BOT"},
		"from a bot":              {msg: func() *discordgo.Message { m := message("", "x"); m.Author.Bot = true; return m }(), botID: "BOT"},
		"from itself":             {msg: func() *discordgo.Message { m := message("", "x"); m.Author.ID = "BOT"; return m }(), botID: "BOT"},
		"from a webhook":          {msg: func() *discordgo.Message { m := message("", "x"); m.WebhookID = "W1"; return m }(), botID: "BOT"},
		"system message": {msg: func() *discordgo.Message {
			m := message("", "x")
			m.Type = discordgo.MessageTypeGuildMemberJoin
			return m
		}(), botID: "BOT"},
		"missing channel":          {msg: func() *discordgo.Message { m := message("", "x"); m.ChannelID = ""; return m }(), botID: "BOT"},
		"missing message id":       {msg: func() *discordgo.Message { m := message("", "x"); m.ID = ""; return m }(), botID: "BOT"},
		"direct thread lookup off": {msg: func() *discordgo.Message { m := message("", "x"); m.ChannelID = "T1"; return m }(), botID: "BOT", wantOK: true, want: inbound{channel: "T1", message: "M1", user: "U1", text: "x"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := parseMessage(tc.msg, tc.botID, threads)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func Test_inbound_conversation(t *testing.T) {
	tests := map[string]struct {
		in   inbound
		want string
	}{
		"channel message": {in: inbound{guild: "G1", channel: "C1", message: "M1"}, want: "discord:G1:M1"},
		"thread":          {in: inbound{guild: "G1", channel: "T1", message: "M1", thread: true}, want: "discord:G1:T1"},
		"direct message":  {in: inbound{channel: "D1", message: "M1"}, want: "discord:@me:M1"},
		// A thread started on a message takes the message's ID, so both keys match.
		"thread from message": {in: inbound{guild: "G1", channel: "M1", message: "M2", thread: true}, want: "discord:G1:M1"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.in.conversation())
		})
	}
}

func Test_stripMention(t *testing.T) {
	tests := map[string]struct {
		in, want  string
		mentioned bool
	}{
		"user mention":     {in: "<@B> hi", want: " hi", mentioned: true},
		"nickname mention": {in: "<@!B>\thi", want: "\thi", mentioned: true},
		"alone":            {in: "  <@B>  ", want: "", mentioned: true},
		"glued":            {in: "<@B>hi", want: "<@B>hi"},
		"other bot":        {in: "<@C> hi", want: "<@C> hi"},
		"not leading":      {in: "hi <@B>", want: "hi <@B>"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := stripMention(tc.in, "B")
			require.Equal(t, tc.mentioned, ok)
			require.Equal(t, tc.want, got)
		})
	}
}
