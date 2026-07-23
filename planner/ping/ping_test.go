package ping_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/planner/ping"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// pingPlan is the plan invoking the ping tool.
func pingPlan() planner.Plan {
	return planner.Plan{Steps: []planner.Step{
		{Tool: "ping://", Call: tool.Call{}},
	}}
}

// replyPlan is the plan posting text back to the requester. The target
// conversation is injected by the executor, so the plan carries only the
// text; conv names the conversation each case operates in for readability.
func replyPlan(_, text string, choices ...tool.Choice) planner.Plan {
	return planner.Plan{Steps: []planner.Step{
		{Tool: reply.URL, Call: tool.Call{
			Arguments: map[string]string{"text": text},
			Choices:   choices,
		}},
	}}
}

const (
	ask     = "do you want me to ping? (yes/no)"
	decline = "ok, I will not ping"
	unknown = "sorry, I don't understand"
)

var confirmationChoices = []tool.Choice{
	{Label: "Yes", Value: "yes"},
	{Label: "No", Value: "no"},
}

func confirmationPlan(conv string) planner.Plan {
	return replyPlan(conv, ask, confirmationChoices...)
}

func Test_Opener_via_registry(t *testing.T) {
	reg := planner.NewRegistry(planner.Backend{Scheme: ping.Scheme, Opener: ping.Opener})

	testCases := map[string]struct {
		url    string
		errMsg string
	}{
		"bare-url":    {url: "ping://"},
		"scheme-only": {url: "ping:"},
		// url.Parse drops a bare trailing "#", making it
		// indistinguishable from the bare URL, so it is accepted.
		"empty-fragment": {url: "ping://#"},
		"host":           {url: "ping://somehost", errMsg: "takes no endpoint"},
		"host-port":      {url: "ping://somehost:1234", errMsg: "takes no endpoint"},
		"path":           {url: "ping:///some/path", errMsg: "takes no endpoint"},
		"query":          {url: "ping://?region=x", errMsg: "takes no endpoint"},
		"empty-query":    {url: "ping://?", errMsg: "takes no endpoint"},
		"userinfo":       {url: "ping://secret@", errMsg: "takes no endpoint"},
		"fragment":       {url: "ping://#frag", errMsg: "takes no endpoint"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			p, err := reg.Open(context.Background(), tc.url, nil, nil)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.NoError(t, p.Close())
		})
	}
}

// exchange is one message sent into a conversation and the plan it
// must produce, given everything sent before it. connID is the chat
// connection the message arrived on; most cases use a single
// connection and leave it empty.
type exchange struct {
	connID string
	conv   string
	text   string
	want   planner.Plan
}

func Test_Plan(t *testing.T) {
	testCases := map[string][]exchange{
		"exact-ping": {
			{conv: "c1", text: "ping", want: pingPlan()},
		},
		"exact-ping-case-and-space": {
			{conv: "c1", text: "  PiNg\n", want: pingPlan()},
		},
		"unrelated-message": {
			{conv: "c1", text: "restart the web server", want: replyPlan("c1", unknown)},
		},
		"yes-without-pending-confirmation": {
			{conv: "c1", text: "yes", want: replyPlan("c1", unknown)},
		},
		"ping-word-asks-then-yes": {
			{conv: "c1", text: "can you ping the box?", want: confirmationPlan("c1")},
			{conv: "c1", text: "yes", want: pingPlan()},
			// the confirmation is consumed; another yes means nothing
			{conv: "c1", text: "yes", want: replyPlan("c1", unknown)},
		},
		"confirmation-shorthand-and-case": {
			{conv: "c1", text: "please ping it", want: confirmationPlan("c1")},
			{conv: "c1", text: " Y ", want: pingPlan()},
		},
		"declined-confirmation": {
			{conv: "c1", text: "ping it please", want: confirmationPlan("c1")},
			{conv: "c1", text: "no", want: replyPlan("c1", decline)},
			{conv: "c1", text: "yes", want: replyPlan("c1", unknown)},
		},
		"declined-confirmation-shorthand": {
			{conv: "c1", text: "ping it please", want: confirmationPlan("c1")},
			{conv: "c1", text: "N", want: replyPlan("c1", decline)},
		},
		"other-topic-drops-pending-confirmation": {
			{conv: "c1", text: "ping it please", want: confirmationPlan("c1")},
			{conv: "c1", text: "how is the weather?", want: replyPlan("c1", unknown)},
			{conv: "c1", text: "yes", want: replyPlan("c1", unknown)},
		},
		"exact-ping-drops-pending-confirmation": {
			{conv: "c1", text: "ping it please", want: confirmationPlan("c1")},
			{conv: "c1", text: "ping", want: pingPlan()},
			{conv: "c1", text: "yes", want: replyPlan("c1", unknown)},
		},
		"repeated-ask-keeps-single-pending-confirmation": {
			{conv: "c1", text: "ping it please", want: confirmationPlan("c1")},
			{conv: "c1", text: "I said ping it", want: confirmationPlan("c1")},
			{conv: "c1", text: "yes", want: pingPlan()},
		},
		"ping-must-be-a-standalone-word": {
			{conv: "c1", text: "pinging the server", want: replyPlan("c1", unknown)},
			{conv: "c1", text: "check shipping status", want: replyPlan("c1", unknown)},
			{conv: "c1", text: "ping? sure", want: confirmationPlan("c1")},
		},
		// Go's \b is ASCII-only; the word boundary must be
		// Unicode-aware so "ping" glued to accented letters, digits,
		// or underscores is not a standalone word.
		"ping-must-be-a-standalone-unicode-word": {
			{conv: "c1", text: "pingé the box", want: replyPlan("c1", unknown)},
			{conv: "c1", text: "run éping now", want: replyPlan("c1", unknown)},
			{conv: "c1", text: "ping2 the box", want: replyPlan("c1", unknown)},
			{conv: "c1", text: "run ping_all", want: replyPlan("c1", unknown)},
			{conv: "c1", text: "é ping é", want: confirmationPlan("c1")},
		},
		"conversations-are-isolated": {
			{conv: "c1", text: "ping the box", want: confirmationPlan("c1")},
			{conv: "c2", text: "yes", want: replyPlan("c2", unknown)},
			{conv: "c1", text: "yes", want: pingPlan()},
		},
		// telnet-style: every connection reports the same
		// conversation ID, so the same conversation ID on another
		// connection must not answer the confirmation.
		"connections-are-isolated": {
			{connID: "connA", conv: "telnet", text: "ping the box", want: confirmationPlan("telnet")},
			{connID: "connB", conv: "telnet", text: "yes", want: replyPlan("telnet", unknown)},
			{connID: "connA", conv: "telnet", text: "yes", want: pingPlan()},
		},
		"empty-message": {
			{conv: "c1", text: "", want: replyPlan("c1", unknown)},
		},
	}

	for name, exchanges := range testCases {
		t.Run(name, func(t *testing.T) {
			p, err := ping.Open(context.Background())
			require.NoError(t, err)
			defer func() {
				require.NoError(t, p.Close())
			}()

			for i, ex := range exchanges {
				req := planner.Request{Text: ex.text, ConnectionID: ex.connID, ConversationID: ex.conv, Sender: "alice"}
				plan, err := p.Plan(context.Background(), req)
				require.NoError(t, err, "exchange %d", i)
				require.Equal(t, ex.want, plan, "exchange %d: %q", i, ex.text)
			}
		})
	}
}

// fakeClock is a manually advanced clock for driving confirmation
// expiry in tests.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func Test_Plan_pending_confirmation_expires(t *testing.T) {
	testCases := map[string]struct {
		age  time.Duration
		want planner.Plan
	}{
		"within-ttl": {age: ping.ConfirmTTLForTest, want: pingPlan()},
		"expired":    {age: ping.ConfirmTTLForTest + time.Second, want: replyPlan("c1", unknown)},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			p, err := ping.Open(context.Background())
			require.NoError(t, err)
			defer func() {
				require.NoError(t, p.Close())
			}()
			clock := &fakeClock{now: time.Now()}
			p.SetNowForTest(clock.Now)

			plan, err := p.Plan(context.Background(), planner.Request{Text: "ping the box", ConversationID: "c1"})
			require.NoError(t, err)
			require.Equal(t, confirmationPlan("c1"), plan)

			clock.now = clock.now.Add(tc.age)
			plan, err = p.Plan(context.Background(), planner.Request{Text: "yes", ConversationID: "c1"})
			require.NoError(t, err)
			require.Equal(t, tc.want, plan)
		})
	}
}

func Test_Plan_expired_confirmations_swept_on_ask(t *testing.T) {
	p, err := ping.Open(context.Background())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, p.Close())
	}()
	clock := &fakeClock{now: time.Now()}
	p.SetNowForTest(clock.Now)

	plan, err := p.Plan(context.Background(), planner.Request{Text: "ping the box", ConversationID: "c1"})
	require.NoError(t, err)
	require.Equal(t, confirmationPlan("c1"), plan)

	// A fresh ask after c1's confirmation expired sweeps it out; the
	// new confirmation works and the expired one does not.
	clock.now = clock.now.Add(ping.ConfirmTTLForTest + time.Second)
	plan, err = p.Plan(context.Background(), planner.Request{Text: "ping the box", ConversationID: "c2"})
	require.NoError(t, err)
	require.Equal(t, confirmationPlan("c2"), plan)

	plan, err = p.Plan(context.Background(), planner.Request{Text: "yes", ConversationID: "c2"})
	require.NoError(t, err)
	require.Equal(t, pingPlan(), plan)
	plan, err = p.Plan(context.Background(), planner.Request{Text: "yes", ConversationID: "c1"})
	require.NoError(t, err)
	require.Equal(t, replyPlan("c1", unknown), plan)
}

func Test_Plan_pending_confirmations_are_capped(t *testing.T) {
	p, err := ping.Open(context.Background())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, p.Close())
	}()
	clock := &fakeClock{now: time.Now()}
	p.SetNowForTest(clock.Now)

	// Ask one confirmation past the cap, each at a distinct instant so
	// the oldest is well-defined.
	for i := range ping.MaxPendingForTest + 1 {
		conv := fmt.Sprintf("c%d", i)
		plan, err := p.Plan(context.Background(), planner.Request{Text: "ping the box", ConversationID: conv})
		require.NoError(t, err)
		require.Equal(t, confirmationPlan(conv), plan)
		clock.now = clock.now.Add(time.Millisecond)
	}

	// The oldest confirmation was evicted to stay within the cap ...
	plan, err := p.Plan(context.Background(), planner.Request{Text: "yes", ConversationID: "c0"})
	require.NoError(t, err)
	require.Equal(t, replyPlan("c0", unknown), plan)

	// ... while newer ones survive.
	for _, conv := range []string{"c1", fmt.Sprintf("c%d", ping.MaxPendingForTest)} {
		plan, err := p.Plan(context.Background(), planner.Request{Text: "yes", ConversationID: conv})
		require.NoError(t, err)
		require.Equal(t, pingPlan(), plan)
	}
}

func Test_Plan_cancelled_context(t *testing.T) {
	p, err := ping.Open(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Plan(ctx, planner.Request{Text: "ping", ConversationID: "c1"})
	require.ErrorIs(t, err, context.Canceled)
}

func Test_Plan_concurrent_conversations(t *testing.T) {
	p, err := ping.Open(context.Background())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, p.Close())
	}()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			conv := fmt.Sprintf("c%d", i)
			for range 50 {
				plan, err := p.Plan(context.Background(), planner.Request{Text: "ping the box", ConversationID: conv})
				require.NoError(t, err)
				require.Equal(t, confirmationPlan(conv), plan)
				plan, err = p.Plan(context.Background(), planner.Request{Text: "yes", ConversationID: conv})
				require.NoError(t, err)
				require.Equal(t, pingPlan(), plan)
			}
		})
	}
	wg.Wait()
}

func Test_Planner_implements_planner_Planner(t *testing.T) {
	var _ planner.Planner = &ping.Planner{}
	require.Equal(t, "ping", ping.Scheme)
}
