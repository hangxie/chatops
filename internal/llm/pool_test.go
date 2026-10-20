package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
)

var (
	errDown     = unavailableError{errors.New("connection refused")}
	errRejected = errors.New("HTTP 400")
)

// fakeEndpoint answers with its name unless err is set; calls records every attempt across endpoints.
type fakeEndpoint struct {
	name  string
	err   error
	calls *[]string
	delay time.Duration
}

func (f *fakeEndpoint) Complete(context.Context, Request) (Response, error) {
	*f.calls = append(*f.calls, f.name)
	time.Sleep(f.delay)
	if f.err != nil {
		return Response{Model: f.name}, f.err
	}
	return Response{Message: Message{Role: RoleAssistant, Content: f.name}, Model: f.name}, nil
}

// testPool builds a pool over endpoints named a, b, c, ...; errs[i] is endpoint i's failure.
func testPool(t *testing.T, schedule string, tries int, errs ...error) (*Pool, *[]string) {
	t.Helper()
	calls := &[]string{}
	endpoints, names := make([]Model, len(errs)), make([]string, len(errs))
	for i, err := range errs {
		names[i] = string(rune('a' + i))
		endpoints[i] = &fakeEndpoint{name: names[i], err: err, calls: calls}
	}
	return newPool(endpoints, names, schedule, tries, slog.New(slog.DiscardHandler)), calls
}

func Test_Session_Complete(t *testing.T) {
	tests := map[string]struct {
		errs     []error
		tries    int
		model    string
		attempts []Attempt
		errMsg   string
	}{
		"first-answers":   {errs: []error{nil, nil}, tries: 2, model: "a", attempts: []Attempt{{Model: "a"}}},
		"fails-over":      {errs: []error{errDown, nil}, tries: 2, model: "b", attempts: []Attempt{{Model: "a", Err: errDown}, {Model: "b"}}},
		"skips-two":       {errs: []error{errDown, errDown, nil}, tries: 3, model: "c", attempts: []Attempt{{Model: "a", Err: errDown}, {Model: "b", Err: errDown}, {Model: "c"}}},
		"rejected-stays":  {errs: []error{errRejected, nil}, tries: 2, model: "a", attempts: []Attempt{{Model: "a", Err: errRejected}}, errMsg: "HTTP 400"},
		"capped-by-tries": {errs: []error{errDown, errDown, nil}, tries: 2, model: "b", attempts: []Attempt{{Model: "a", Err: errDown}, {Model: "b", Err: errDown}}, errMsg: "connection refused"},
		"single-try":      {errs: []error{errDown, nil}, tries: 1, model: "a", attempts: []Attempt{{Model: "a", Err: errDown}}, errMsg: "connection refused"},
		"all-down":        {errs: []error{errDown, errDown}, tries: 2, model: "b", attempts: []Attempt{{Model: "a", Err: errDown}, {Model: "b", Err: errDown}}, errMsg: "connection refused"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			pool, calls := testPool(t, config.ScheduleOrdered, tc.tries, tc.errs...)
			resp, err := pool.Session().Complete(context.Background(), Request{})
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.model, resp.Message.Content)
			}
			require.Equal(t, tc.model, resp.Model)
			require.Equal(t, tc.attempts, withoutAttemptDurations(resp.Attempts))
			calledModels := make([]string, len(tc.attempts))
			for i, attempt := range tc.attempts {
				calledModels[i] = attempt.Model
			}
			require.Equal(t, calledModels, *calls)
		})
	}
}

// withoutAttemptDurations zeroes timings so attempts compare by value.
func withoutAttemptDurations(attempts []Attempt) []Attempt {
	out := make([]Attempt, len(attempts))
	for i, attempt := range attempts {
		attempt.Duration = 0
		out[i] = attempt
	}
	return out
}

func Test_Session_Complete_times_each_attempt(t *testing.T) {
	calls := &[]string{}
	slow := &fakeEndpoint{name: "a", err: errDown, calls: calls, delay: 20 * time.Millisecond}
	pool := newPool([]Model{slow, &fakeEndpoint{name: "b", calls: calls}}, []string{"a", "b"}, config.ScheduleOrdered, 2, slog.New(slog.DiscardHandler))
	resp, err := pool.Session().Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Len(t, resp.Attempts, 2)
	// Each attempt is timed on its own, so the backup is not charged for the primary's wait.
	require.GreaterOrEqual(t, resp.Attempts[0].Duration, 20*time.Millisecond)
	require.Less(t, resp.Attempts[1].Duration, 20*time.Millisecond)
}

func Test_Session_Complete_caller_gave_up(t *testing.T) {
	pool, calls := testPool(t, config.ScheduleOrdered, 2, errDown, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pool.Session().Complete(ctx, Request{})
	require.ErrorIs(t, err, errDown)
	require.Equal(t, []string{"a"}, *calls)
}

func Test_Session_Complete_keeps_failed_over_model(t *testing.T) {
	pool, calls := testPool(t, config.ScheduleOrdered, 2, errDown, nil)
	session := pool.Session()
	for range 2 {
		_, err := session.Complete(context.Background(), Request{})
		require.NoError(t, err)
	}
	// The turn's second call goes straight to the model that answered.
	require.Equal(t, []string{"a", "b", "b"}, *calls)
}

func Test_Pool_Session_schedule(t *testing.T) {
	tests := map[string]struct {
		schedule string
		errs     []error
		calls    []string
	}{
		"ordered":                {schedule: config.ScheduleOrdered, errs: []error{nil, nil, nil}, calls: []string{"a", "a", "a", "a"}},
		"ordered-retries-first":  {schedule: config.ScheduleOrdered, errs: []error{errDown, nil, nil}, calls: []string{"a", "b", "a", "b", "a", "b", "a", "b"}},
		"round-robin":            {schedule: config.ScheduleRoundRobin, errs: []error{nil, nil, nil}, calls: []string{"a", "b", "c", "a"}},
		"round-robin-wraps":      {schedule: config.ScheduleRoundRobin, errs: []error{nil, nil, errDown}, calls: []string{"a", "b", "c", "a", "a"}},
		"sticky":                 {schedule: config.ScheduleSticky, errs: []error{nil, nil, nil}, calls: []string{"a", "a", "a", "a"}},
		"sticky-stays-on-backup": {schedule: config.ScheduleSticky, errs: []error{errDown, nil, nil}, calls: []string{"a", "b", "b", "b", "b"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			pool, calls := testPool(t, tc.schedule, len(tc.errs), tc.errs...)
			for range 4 {
				_, err := pool.Session().Complete(context.Background(), Request{})
				require.NoError(t, err)
			}
			require.Equal(t, tc.calls, *calls)
		})
	}
}

func Test_Pool_Session_concurrent(t *testing.T) {
	pool, _ := testPool(t, config.ScheduleRoundRobin, 3, nil, nil, nil)
	seen := make(chan int, 30)
	for range 30 {
		go func() { seen <- pool.Session().(*session).current }()
	}
	counts := map[int]int{}
	for range 30 {
		counts[<-seen]++
	}
	require.Equal(t, map[int]int{0: 10, 1: 10, 2: 10}, counts)
}

func Test_Session_Complete_logs_failover(t *testing.T) {
	var logs bytes.Buffer
	calls := &[]string{}
	pool := newPool([]Model{
		&fakeEndpoint{name: "a", err: errDown, calls: calls},
		&fakeEndpoint{name: "b", calls: calls},
	}, []string{"a", "b"}, config.ScheduleOrdered, 2, slog.New(slog.NewTextHandler(&logs, nil)))
	_, err := pool.Session().Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Contains(t, logs.String(), `level=WARN msg="model failed over" model=a next=b error="connection refused"`)
}

func Test_NewPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "overloaded", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	backup := httptest.NewServer(reply(textResponse))
	defer backup.Close()

	pool, err := NewPool(config.LLM{
		Models: []config.Model{
			{BaseURL: server.URL, Model: "qwen", Name: "primary", Stream: streamOff()},
			{BaseURL: backup.URL, Model: "qwen", Name: "replica", Stream: streamOff()},
		},
		Schedule:      config.ScheduleOrdered,
		MaxModelRetry: 2,
	}, nil, nil)
	require.NoError(t, err)
	resp, err := pool.Session().Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.NoError(t, err)
	require.Equal(t, Message{Role: RoleAssistant, Content: "hi"}, resp.Message)
	// Two endpoints serving one model stay apart by name.
	require.Equal(t, "replica", resp.Model)
	require.Len(t, resp.Attempts, 2)
	require.Equal(t, "primary", resp.Attempts[0].Model)
	require.ErrorIs(t, resp.Attempts[0].Err, ErrUnavailable)
	require.Equal(t, Attempt{Model: "replica"}, withoutAttemptDurations(resp.Attempts)[1])
}

func Test_NewPool_bad_model(t *testing.T) {
	_, err := NewPool(config.LLM{
		Models:        []config.Model{{BaseURL: "http://h/v1", Model: "a"}, {BaseURL: "ftp://h/v1", Model: "b"}},
		Schedule:      config.ScheduleOrdered,
		MaxModelRetry: 2,
	}, nil, nil)
	require.EqualError(t, err, fmt.Sprintf("llm.models[1]: %s", `llm base URL "ftp://h/v1" must use http or https`))
}
