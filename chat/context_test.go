package chat_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
)

func Test_WithConversation(t *testing.T) {
	testCases := map[string]struct {
		ctx    func() context.Context
		want   string
		wantOK bool
	}{
		"carried": {
			ctx:    func() context.Context { return chat.WithConversation(context.Background(), "conv-1") },
			want:   "conv-1",
			wantOK: true,
		},
		"empty-is-still-carried": {
			ctx:    func() context.Context { return chat.WithConversation(context.Background(), "") },
			want:   "",
			wantOK: true,
		},
		"absent": {
			ctx:    context.Background,
			want:   "",
			wantOK: false,
		},
		"innermost-wins": {
			ctx: func() context.Context {
				ctx := chat.WithConversation(context.Background(), "outer")
				return chat.WithConversation(ctx, "inner")
			},
			want:   "inner",
			wantOK: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, ok := chat.ConversationFrom(tc.ctx())
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func Test_WithConversation_survives_derived_context(t *testing.T) {
	ctx := chat.WithConversation(context.Background(), "conv-1")
	derived, cancel := context.WithCancel(ctx)
	defer cancel()

	got, ok := chat.ConversationFrom(derived)
	require.True(t, ok)
	require.Equal(t, "conv-1", got)
}
