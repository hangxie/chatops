package discord

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/conversation"
)

func Test_threadName(t *testing.T) {
	tests := map[string]struct{ in, want string }{
		"short":      {in: "is it up?", want: "is it up?"},
		"first line": {in: "check nodes\nand pods", want: "check nodes"},
		"trimmed":    {in: "  status \r\nmore", want: "status"},
		"long":       {in: strings.Repeat("é", 150), want: strings.Repeat("é", maxThreadName)},
		"empty":      {in: "\nsecond", want: "chatops"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, threadName(tc.in))
		})
	}
}

func Test_Adapter_threadReplier(t *testing.T) {
	alreadyCreated := &discordgo.RESTError{Message: &discordgo.APIErrorMessage{Code: discordgo.ErrCodeThreadAlreadyCreatedForThisMessage}}
	tests := map[string]struct {
		err         error
		transient   bool
		wantChannel string
		wantRef     bool
	}{
		"started":         {wantChannel: "M1"},
		"already started": {err: alreadyCreated, wantChannel: "M1"},
		"no permission":   {err: errors.New("Missing Permissions"), wantChannel: "C1", wantRef: true},
		// No retry: a later thread would split one answer between the channel and the thread.
		"transient failure": {err: context.DeadlineExceeded, transient: true, wantChannel: "C1", wantRef: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gw := newFakeGateway()
			gw.threadErr, gw.threadOnce = tc.err, tc.transient
			a := newAdapter(gw, nil)
			ref := &discordgo.MessageReference{MessageID: "M1"}
			reply := a.threadReplier(inbound{guild: "G1", channel: "C1", message: "M1", text: "status"}, ref, a.logger)

			for range 2 {
				require.NoError(t, reply(context.Background(), conversation.Reply{Text: "ok"}))
				got := gw.nextSend(t)
				require.Equal(t, tc.wantChannel, got.channel)
				if tc.wantRef {
					require.Same(t, ref, got.msg.Reference)
				} else {
					require.Nil(t, got.msg.Reference)
				}
			}
			require.Equal(t, threadStart{channel: "C1", message: "M1", name: "status"}, next(t, gw.started))
			require.Empty(t, gw.started, "the thread is started once")
		})
	}
}
