package discord

import (
	"strings"

	"github.com/bwmarrin/discordgo"
)

// inbound is a user message addressed to the bot, with the leading mention removed.
type inbound struct {
	guild, channel, message, user, text string
	thread                              bool
}

// conversation keys history by thread, or by message outside one; a thread started on a message shares its ID.
func (in inbound) conversation() string {
	guild := in.guild
	if guild == "" {
		guild = "@me"
	}
	if in.thread {
		return "discord:" + guild + ":" + in.channel
	}
	return "discord:" + guild + ":" + in.message
}

// parseMessage accepts user messages in channels that lead with a bot mention and any direct message.
func parseMessage(m *discordgo.Message, botID string, isThread func(string) bool) (inbound, bool) {
	if m == nil || m.Author == nil || m.Author.Bot || m.WebhookID != "" || botID == "" || m.Author.ID == botID {
		return inbound{}, false
	}
	if m.Type != discordgo.MessageTypeDefault && m.Type != discordgo.MessageTypeReply {
		return inbound{}, false
	}
	direct := m.GuildID == ""
	text, mentioned := stripMention(m.Content, botID)
	if !mentioned && !direct {
		return inbound{}, false
	}
	in := inbound{guild: m.GuildID, channel: m.ChannelID, message: m.ID, user: m.Author.ID, text: strings.TrimSpace(text)}
	if in.channel == "" || in.message == "" || in.text == "" {
		return inbound{}, false
	}
	in.thread = !direct && isThread(in.channel)
	return in, true
}

// stripMention removes a leading "<@bot>" or "<@!bot>" that is followed by whitespace or nothing.
func stripMention(text, botID string) (string, bool) {
	text = strings.TrimSpace(text)
	for _, mention := range []string{"<@" + botID + ">", "<@!" + botID + ">"} {
		rest, ok := strings.CutPrefix(text, mention)
		if ok && (rest == "" || strings.ContainsAny(rest[:1], " \t\r\n")) {
			return rest, true
		}
	}
	return text, false
}
