package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
)

func Test_messageScheduler_preserves_conversation_order(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var handled []string
	s := newMessageScheduler(ctx, 2, func(msg chat.Message) error {
		mu.Lock()
		defer mu.Unlock()
		handled = append(handled, msg.Text)
		return nil
	})
	for _, text := range []string{"first", "second", "third"} {
		require.NoError(t, s.Submit(chat.Message{ConversationID: "c1", Text: text}))
	}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 3
	}, time.Second, time.Millisecond)
	s.Stop()
	require.NoError(t, s.Wait())
	require.Equal(t, []string{"first", "second", "third"}, handled)
}

func Test_messageScheduler_bounds_parallel_work(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 3)
	s := newMessageScheduler(ctx, 2, func(chat.Message) error {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return nil
	})
	for _, conversationID := range []string{"c1", "c2", "c3"} {
		require.NoError(t, s.Submit(chat.Message{ConversationID: conversationID}))
	}
	<-started
	<-started
	require.Never(t, func() bool { return len(started) == 1 }, 50*time.Millisecond, time.Millisecond)
	close(release)
	require.Eventually(t, func() bool { return len(started) == 1 }, time.Second, time.Millisecond)
	s.Stop()
	require.NoError(t, s.Wait())
	require.Equal(t, int32(2), maximum.Load())
}

func Test_messageScheduler_submit_after_stop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newMessageScheduler(ctx, 1, func(chat.Message) error { return nil })
	cancel()
	<-s.Done()
	require.ErrorIs(t, s.Submit(chat.Message{}), context.Canceled)
	require.NoError(t, s.Wait())
}

func Test_messageScheduler_propagates_failure_to_submit(t *testing.T) {
	testErr := errors.New("worker failed")
	s := newMessageScheduler(context.Background(), 1, func(chat.Message) error { return testErr })
	require.NoError(t, s.Submit(chat.Message{ConversationID: "c1"}))
	<-s.Done()
	require.ErrorIs(t, s.Submit(chat.Message{ConversationID: "c2"}), testErr)
	require.ErrorIs(t, s.Wait(), testErr)
}

func Test_messageScheduler_shutdown_unblocks_full_backlog(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s := newMessageScheduler(context.Background(), 1, func(chat.Message) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return nil
	})
	require.NoError(t, s.Submit(chat.Message{ConversationID: "active"}))
	<-started
	for i := range maxPendingMessages {
		require.NoError(t, s.Submit(chat.Message{ConversationID: "queued", Text: fmt.Sprint(i)}))
	}
	submitResult := make(chan error, 1)
	go func() { submitResult <- s.Submit(chat.Message{ConversationID: "blocked"}) }()
	require.Never(t, func() bool { return len(submitResult) == 1 }, 50*time.Millisecond, time.Millisecond)
	s.Stop()
	require.ErrorIs(t, <-submitResult, context.Canceled)
	close(release)
	require.NoError(t, s.Wait())
}

func Test_messageScheduler_stop_records_error(t *testing.T) {
	testErr := errors.New("stop failed")
	s := newMessageScheduler(context.Background(), 1, func(chat.Message) error { return nil })
	s.stop(testErr)
	require.ErrorIs(t, s.Wait(), testErr)
}
