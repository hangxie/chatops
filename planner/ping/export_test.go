package ping

import "time"

// SetNowForTest overrides the planner's clock so tests can control
// confirmation expiry deterministically.
func (p *Planner) SetNowForTest(now func() time.Time) {
	p.now = now
}

// Internal limits exposed for tests.
const (
	ConfirmTTLForTest = confirmTTL
	MaxPendingForTest = maxPending
)
