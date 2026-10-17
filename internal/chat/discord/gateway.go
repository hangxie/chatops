package discord

import (
	"context"
	"sync"

	"github.com/bwmarrin/discordgo"
)

// intents covers mentions in guild channels and direct messages without the privileged message content intent.
const intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages

// gateway is the slice of a Discord session the adapter uses, so tests can fake it.
type gateway interface {
	// Open connects and identifies; events flow on Events, READY included, while it runs.
	Open() error
	Close() error
	Events() <-chan any
	// IsThread reports whether channelID is a thread, asking the REST API when the state cache lacks it.
	IsThread(ctx context.Context, channelID string) (bool, error)
	Send(ctx context.Context, channelID string, msg *discordgo.MessageSend) error
	// StartThread opens a public thread on a message and returns the thread's ID.
	StartThread(ctx context.Context, channelID, messageID, name string) (string, error)
}

// session forwards a discordgo session's events, in gateway order, to one channel.
type session struct {
	s      *discordgo.Session
	events chan any
	done   chan struct{}
	once   sync.Once
}

func newSession(token string) (*session, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = intents
	// Synchronous handlers keep messages in order; forward only blocks until the adapter reads or closes.
	s.SyncEvents = true
	gw := &session{s: s, events: make(chan any), done: make(chan struct{})}
	s.AddHandler(func(_ *discordgo.Session, event any) { gw.forward(event) })
	return gw, nil
}

// forward passes on the events the adapter handles, dropping them once the session is closed.
func (g *session) forward(event any) {
	switch event.(type) {
	case *discordgo.Ready, *discordgo.Resumed, *discordgo.Disconnect, *discordgo.MessageCreate:
	default:
		return
	}
	select {
	case g.events <- event:
	case <-g.done:
	}
}

func (g *session) Open() error        { return g.s.Open() }
func (g *session) Events() <-chan any { return g.events }

// Close releases a blocked forward before closing the websocket, which waits for the event loop.
func (g *session) Close() error {
	g.once.Do(func() { close(g.done) })
	return g.s.Close()
}

// IsThread falls back to REST because a thread revived from the archive may post before its update event.
func (g *session) IsThread(ctx context.Context, channelID string) (bool, error) {
	if channel, err := g.s.State.Channel(channelID); err == nil {
		return channel.IsThread(), nil
	}
	channel, err := g.s.Channel(channelID, discordgo.WithContext(ctx))
	if err != nil {
		return false, err
	}
	// Best effort: the add fails when the channel's guild is not cached, and the next miss asks again.
	_ = g.s.State.ChannelAdd(channel)
	return channel.IsThread(), nil
}

func (g *session) Send(ctx context.Context, channelID string, msg *discordgo.MessageSend) error {
	_, err := g.s.ChannelMessageSendComplex(channelID, msg, discordgo.WithContext(ctx))
	return err
}

func (g *session) StartThread(ctx context.Context, channelID, messageID, name string) (string, error) {
	thread, err := g.s.MessageThreadStartComplex(channelID, messageID, &discordgo.ThreadStart{Name: name, AutoArchiveDuration: threadArchiveMinutes}, discordgo.WithContext(ctx))
	if err != nil {
		return "", err
	}
	return thread.ID, nil
}
