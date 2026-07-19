// Package ping implements a dummy planner.Planner that recognizes
// only the ping intent, useful as a wiring check and as the reference
// implementation of the planner interface.
//
// The package exports Scheme and Opener for wiring the planner into a
// planner.Registry under the "ping" URL scheme. The planner talks to
// no LLM endpoint and takes no credentials, so the URL is bare:
//
//	ping://
//
// A message that is exactly "ping" (ignoring case and surrounding
// whitespace) plans an invocation of the ping tool. A message that
// merely contains "ping" as a standalone word plans a reply asking
// for confirmation and remembers the pending question; each
// conversation — scoped by the request's connection and conversation
// IDs, so the same conversation ID on another chat connection cannot
// answer it — holds at most one pending confirmation, and a repeated
// ask just renews it. The next message in that conversation answers
// it: "yes"/"y" plans the ping, "no"/"n" plans an acknowledging
// reply, and anything else drops the pending confirmation without
// pinging and is handled as a fresh message. Everything unrecognized
// plans a reply saying the planner does not understand.
//
// Pending confirmations are bounded state: an unanswered confirmation
// expires after ten minutes, and at most 1024 conversations'
// confirmations are remembered at once (asking past the cap evicts
// the oldest), so a long-running bot does not accumulate abandoned
// conversations.
package ping

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	toolping "github.com/hangxie/chatops/tool/ping"
	"github.com/hangxie/chatops/tool/reply"
)

// Scheme is the URL scheme this planner serves in a planner.Registry.
const Scheme = "ping"

// Tool URLs the planner emits in plan steps, per the schemes exported
// by the tools themselves.
const (
	pingToolURL  = toolping.Scheme + "://"
	replyToolURL = reply.URL
)

// Reply texts the planner posts back to the requester.
const (
	askText     = "do you want me to ping? (yes/no)"
	declineText = "ok, I will not ping"
	unknownText = "sorry, I don't understand"
)

// pingWordRE matches "ping" as a standalone word — not adjoining a
// letter, digit, or underscore — so "can you ping the box?" asks for
// confirmation while "pinging", "shipping", or "pingé" do not. Go's
// \b knows only ASCII word characters, so the boundaries are spelled
// out with Unicode classes.
var pingWordRE = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}_])ping($|[^\p{L}\p{N}_])`)

// Bounds on remembered pending confirmations, so abandoned
// conversations do not accumulate in a long-running bot.
const (
	// confirmTTL is how long an unanswered confirmation stays valid.
	confirmTTL = 10 * time.Minute

	// maxPending is the maximum number of conversations with a
	// remembered confirmation; asking past it evicts the oldest.
	maxPending = 1024
)

// Opener is the planner.OpenerFunc for this planner: the URL carries
// no endpoint or configuration, and creds is ignored. Any host, path,
// query, userinfo, or non-empty fragment is rejected; a bare trailing
// "#" is parsed by net/url identically to the bare URL and is
// therefore accepted.
func Opener(ctx context.Context, u *url.URL, _ cred.Store) (planner.Planner, error) {
	if u.Host != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("ping: URL %q takes no endpoint or configuration", u.String())
	}
	return Open(ctx)
}

// convKey identifies one conversation for pending-confirmation
// state. Conversation IDs are only unique within one chat
// connection, so the key carries the request's connection ID too.
type convKey struct {
	connection   string
	conversation string
}

// Planner is the dummy ping planner. It keeps the pending
// confirmation question per conversation and is safe for concurrent
// use.
type Planner struct {
	mu sync.Mutex
	// now is the planner's clock; it exists to drive confirmation
	// expiry deterministically in tests.
	now func() time.Time
	// pending maps each conversation with an unanswered "do you want
	// me to ping?" question — at most one per conversation — to when
	// it was asked, for expiry and oldest-first eviction.
	pending map[convKey]time.Time
}

// Open returns a ready ping planner; it holds no external resources
// and needs no location parameters.
func Open(_ context.Context) (*Planner, error) {
	return &Planner{now: time.Now, pending: map[convKey]time.Time{}}, nil
}

// Plan maps req.Text onto a ping tool step, a confirmation-question
// reply step, or an "I don't understand" reply step, per the package
// documentation. It never returns an error other than the context's.
func (p *Planner) Plan(ctx context.Context, req planner.Request) (planner.Plan, error) {
	if err := ctx.Err(); err != nil {
		return planner.Plan{}, fmt.Errorf("ping: %w", err)
	}

	text := strings.ToLower(strings.TrimSpace(req.Text))
	conv := req.ConversationID
	key := convKey{connection: req.ConnectionID, conversation: conv}

	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	askedAt, exists := p.pending[key]
	wasPending := exists && now.Sub(askedAt) <= confirmTTL
	// Any message resolves or drops the pending confirmation; only a
	// fresh ask below re-arms it.
	delete(p.pending, key)
	if wasPending && (text == "yes" || text == "y") {
		return pingPlan(), nil
	}
	if wasPending && (text == "no" || text == "n") {
		return replyPlan(conv, declineText), nil
	}
	if text == "ping" {
		return pingPlan(), nil
	}
	if pingWordRE.MatchString(text) {
		p.evictStale(now)
		p.pending[key] = now
		return replyPlan(conv, askText), nil
	}
	return replyPlan(conv, unknownText), nil
}

// evictStale drops expired confirmations and then the oldest ones
// until there is room under maxPending for one more. The caller must
// hold p.mu.
func (p *Planner) evictStale(now time.Time) {
	for key, askedAt := range p.pending {
		if now.Sub(askedAt) > confirmTTL {
			delete(p.pending, key)
		}
	}
	for len(p.pending) >= maxPending {
		oldest, oldestAt := convKey{}, now
		for key, askedAt := range p.pending {
			if !askedAt.After(oldestAt) {
				oldest, oldestAt = key, askedAt
			}
		}
		delete(p.pending, oldest)
	}
}

// Close releases nothing external; it only drops the pending
// confirmation state.
func (p *Planner) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = map[convKey]time.Time{}
	return nil
}

// pingPlan is the single-step plan invoking the ping tool.
func pingPlan() planner.Plan {
	return planner.Plan{Steps: []planner.Step{
		{Tool: pingToolURL, Call: tool.Call{Action: "ping"}},
	}}
}

// replyPlan is the single-step plan posting text into conversation
// conv through the reply tool.
func replyPlan(conv, text string) planner.Plan {
	return planner.Plan{Steps: []planner.Step{
		{Tool: replyToolURL, Call: tool.Call{
			Action:     "send",
			Target:     conv,
			Parameters: map[string]string{"text": text},
		}},
	}}
}
