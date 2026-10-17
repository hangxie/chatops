package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"
)

func Test_newSession(t *testing.T) {
	gw, err := newSession("token")
	require.NoError(t, err)
	require.Equal(t, "Bot token", gw.s.Token)
	require.Equal(t, intents, gw.s.Identify.Intents)
	require.True(t, gw.s.SyncEvents)
	require.NotNil(t, gw.s.State)
}

func Test_session_forward(t *testing.T) {
	tests := map[string]struct {
		event     any
		forwarded bool
	}{
		"ready":          {event: &discordgo.Ready{}, forwarded: true},
		"resumed":        {event: &discordgo.Resumed{}, forwarded: true},
		"disconnect":     {event: &discordgo.Disconnect{}, forwarded: true},
		"message create": {event: &discordgo.MessageCreate{Message: &discordgo.Message{}}, forwarded: true},
		"connect":        {event: &discordgo.Connect{}},
		"message update": {event: &discordgo.MessageUpdate{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gw, err := newSession("token")
			require.NoError(t, err)
			done := make(chan struct{})
			go func() {
				defer close(done)
				gw.forward(tc.event)
			}()
			if tc.forwarded {
				select {
				case got := <-gw.Events():
					require.Same(t, tc.event, got)
				case <-time.After(5 * time.Second):
					t.Fatal("event not forwarded")
				}
			}
			<-done
		})
	}
}

func Test_session_forward_after_close(t *testing.T) {
	gw, err := newSession("token")
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	require.NoError(t, gw.Close())
	gw.forward(&discordgo.Ready{})
}

func Test_session_IsThread(t *testing.T) {
	lookups := make(chan string, 4)
	fakeDiscordAPI(t, func(rw http.ResponseWriter, r *http.Request) {
		lookups <- r.URL.Path
		rw.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/channels/T2":
			_, _ = rw.Write([]byte(`{"id":"T2","guild_id":"G1","type":11}`))
		case "/channels/C2":
			_, _ = rw.Write([]byte(`{"id":"C2","guild_id":"G1","type":0}`))
		default:
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write([]byte(`{"code":10003,"message":"Unknown Channel"}`))
		}
	})
	gw, err := newSession("token")
	require.NoError(t, err)
	require.NoError(t, gw.s.State.GuildAdd(&discordgo.Guild{ID: "G1"}))
	require.NoError(t, gw.s.State.ChannelAdd(&discordgo.Channel{ID: "C1", GuildID: "G1", Type: discordgo.ChannelTypeGuildText}))
	require.NoError(t, gw.s.State.ChannelAdd(&discordgo.Channel{ID: "T1", GuildID: "G1", Type: discordgo.ChannelTypeGuildPublicThread}))
	ctx := context.Background()

	tests := map[string]struct {
		channel string
		want    bool
		wantErr string
		lookup  bool
	}{
		"cached thread":         {channel: "T1", want: true},
		"cached channel":        {channel: "C1"},
		"uncached thread":       {channel: "T2", want: true, lookup: true},
		"uncached thread again": {channel: "T2", want: true},
		"uncached channel":      {channel: "C2", lookup: true},
		"unknown channel":       {channel: "X1", wantErr: "Unknown Channel", lookup: true},
	}
	for _, name := range []string{"cached thread", "cached channel", "uncached thread", "uncached thread again", "uncached channel", "unknown channel"} {
		tc := tests[name]
		t.Run(name, func(t *testing.T) {
			got, err := gw.IsThread(ctx, tc.channel)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.want, got)
			if tc.lookup {
				require.Equal(t, "/channels/"+tc.channel, <-lookups)
			}
			require.Empty(t, lookups)
		})
	}
}

// fakeDiscordAPI points discordgo's REST endpoints at handler for the test's duration.
func fakeDiscordAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	channels, gatewayURL := discordgo.EndpointChannels, discordgo.EndpointGateway
	discordgo.EndpointChannels, discordgo.EndpointGateway = server.URL+"/channels/", server.URL+"/gateway"
	t.Cleanup(func() { discordgo.EndpointChannels, discordgo.EndpointGateway = channels, gatewayURL })
}

func Test_session_Send(t *testing.T) {
	type request struct {
		path string
		body map[string]any
	}
	requests := make(chan request, 1)
	fakeDiscordAPI(t, func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests <- request{path: r.URL.Path, body: body}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"id":"M2","channel_id":"C1"}`))
	})
	gw, err := newSession("token")
	require.NoError(t, err)

	err = gw.Send(context.Background(), "C1", &discordgo.MessageSend{
		Content:         "hi",
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
		Flags:           discordgo.MessageFlagsSuppressEmbeds,
	})
	require.NoError(t, err)
	got := <-requests
	require.Equal(t, "/channels/C1/messages", got.path)
	require.Equal(t, "hi", got.body["content"])
	require.EqualValues(t, discordgo.MessageFlagsSuppressEmbeds, got.body["flags"])
	require.Equal(t, map[string]any{"parse": []any{}, "replied_user": false}, got.body["allowed_mentions"])
}

func Test_session_Send_error(t *testing.T) {
	fakeDiscordAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte(`{"code":50013,"message":"Missing Permissions"}`))
	})
	gw, err := newSession("token")
	require.NoError(t, err)
	require.ErrorContains(t, gw.Send(context.Background(), "C1", &discordgo.MessageSend{Content: "hi"}), "Missing Permissions")
}

func Test_session_Open_error(t *testing.T) {
	fakeDiscordAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusUnauthorized)
		_, _ = rw.Write([]byte(`{"message":"401: Unauthorized","code":0}`))
	})
	gw, err := newSession("token")
	require.NoError(t, err)
	require.ErrorContains(t, gw.Open(), "401")
}
