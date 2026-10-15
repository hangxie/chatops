package conversation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Reply_Plain(t *testing.T) {
	tests := map[string]struct {
		in   Reply
		want string
	}{
		"text only":   {in: Reply{Text: "pong"}, want: "pong"},
		"with footer": {in: Reply{Text: "pong", Footer: "answer 1.0s"}, want: "pong\n\nanswer 1.0s"},
		"empty":       {in: Reply{}, want: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.in.Plain())
		})
	}
}
