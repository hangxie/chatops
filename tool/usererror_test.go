package tool

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_NewUserError(t *testing.T) {
	err := NewUserError("no resource type %q", "foo")
	require.EqualError(t, err, `no resource type "foo"`)

	msg, ok := UserMessage(err)
	require.True(t, ok)
	require.Equal(t, `no resource type "foo"`, msg)
}

func Test_UserMessage(t *testing.T) {
	t.Run("plain error is not user-facing", func(t *testing.T) {
		msg, ok := UserMessage(errors.New("internal https://host/path failed"))
		require.False(t, ok)
		require.Empty(t, msg)
	})

	t.Run("nil is not user-facing", func(t *testing.T) {
		msg, ok := UserMessage(nil)
		require.False(t, ok)
		require.Empty(t, msg)
	})

	t.Run("wrapped user error is unwrapped", func(t *testing.T) {
		base := NewUserError("not authorized")
		wrapped := fmt.Errorf("k8s: list applications: %w", base)
		msg, ok := UserMessage(wrapped)
		require.True(t, ok)
		require.Equal(t, "not authorized", msg)
	})
}

func Test_userError_Unwrap(t *testing.T) {
	cause := errors.New("boom")
	err := WrapUserError(cause, "could not reach the cluster")
	require.ErrorIs(t, err, cause)
	// Error joins the safe message with the cause; UserMessage stays clean.
	require.EqualError(t, err, "could not reach the cluster: boom")

	msg, ok := UserMessage(err)
	require.True(t, ok)
	require.Equal(t, "could not reach the cluster", msg)
}
