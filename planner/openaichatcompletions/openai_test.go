package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// fakeCreds is a minimal cred.Store for exercising API-key resolution.
type fakeCreds struct {
	values map[string]string
	err    error
}

func (f fakeCreds) Get(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if v, ok := f.values[key]; ok {
		return v, nil
	}
	return "", cred.ErrNotFound
}

func (fakeCreds) Close() error { return nil }

func fakeToolOpener(_ context.Context, _ *url.URL, _ cred.Store) (tool.Tool, error) {
	return nil, nil
}

func openerViaRegistry(t *testing.T, rawURL string, creds cred.Store, tools *tool.Registry) (planner.Planner, error) {
	t.Helper()
	reg := planner.NewRegistry(planner.Backend{Scheme: Scheme, Opener: Opener})
	return reg.Open(context.Background(), rawURL, creds, tools)
}

func Test_Opener_parses_endpoint_and_model(t *testing.T) {
	testCases := map[string]struct {
		url         string
		wantBaseURL string
		wantModel   string
	}{
		"host-default-path": {
			url: "openai-chat-completions://api.openai.com?model=gpt-5", wantBaseURL: "https://api.openai.com/v1", wantModel: "gpt-5",
		},
		"host-and-path": {
			url:         "openai-chat-completions://generativelanguage.googleapis.com/v1beta/openai?model=gemini-3.1-flash-lite",
			wantBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
			wantModel:   "gemini-3.1-flash-lite",
		},
		"insecure-local": {
			url: "openai-chat-completions://localhost:11434/v1?insecure=true&model=llama3", wantBaseURL: "http://localhost:11434/v1", wantModel: "llama3",
		},
		"trailing-slash-trimmed": {
			url: "openai-chat-completions://host/v1/?model=m", wantBaseURL: "https://host/v1", wantModel: "m",
		},
		"encoded-path-preserved": {
			url: "openai-chat-completions://host/v1%3Ftenant?model=m", wantBaseURL: "https://host/v1%3Ftenant", wantModel: "m",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			opened, err := openerViaRegistry(t, tc.url, nil, nil)
			require.NoError(t, err)
			p := opened.(*Planner)
			require.Equal(t, tc.wantBaseURL, p.baseURL)
			require.Equal(t, tc.wantModel, p.model)
			require.Empty(t, p.apiKey)
			require.NoError(t, p.Close())
		})
	}
}

func Test_Opener_rejects_invalid_url(t *testing.T) {
	testCases := map[string]struct {
		url    string
		errMsg string
	}{
		"missing-model": {url: "openai-chat-completions://host", errMsg: "missing the required model"},
		"blank-model":   {url: "openai-chat-completions://host?model=%20", errMsg: "missing the required model"},
		"userinfo":      {url: "openai-chat-completions://secret@host?model=m", errMsg: "must not carry"},
		"fragment":      {url: "openai-chat-completions://host?model=m#frag", errMsg: "must not carry"},
		"opaque":        {url: "openai-chat-completions:opaque?model=m", errMsg: "must not carry"},
		"no-host":       {url: "openai-chat-completions://?model=m", errMsg: "must specify the endpoint host"},
		"no-host-path":  {url: "openai-chat-completions:///v1?model=m", errMsg: "must specify the endpoint host"},
		"no-host-insec": {url: "openai-chat-completions://?insecure=true&model=m", errMsg: "must specify the endpoint host"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := openerViaRegistry(t, tc.url, nil, nil)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Opener_offers_enabled_tool_actions(t *testing.T) {
	statusDesc := tool.Descriptor{Summary: "status", Actions: []tool.Action{{Name: "list"}}}
	pingDesc := tool.Descriptor{Summary: "ping", Actions: []tool.Action{{Name: "ping"}}}
	tools := tool.NewRegistry(
		tool.Backend{Scheme: "status", Opener: fakeToolOpener, Descriptor: &statusDesc},
		tool.Backend{Scheme: "ping", Opener: fakeToolOpener, Descriptor: &pingDesc},
	)
	opened, err := openerViaRegistry(t, "openai-chat-completions://host/v1?model=m", nil, tools)
	require.NoError(t, err)
	defer func() { require.NoError(t, opened.Close()) }()
	planner := opened.(*Planner)
	// Every enabled tool's actions become offered functions keyed back to
	// their (scheme, action).
	require.Equal(t, "status", planner.funcs["status-list"].scheme)
	require.Equal(t, "list", planner.funcs["status-list"].action.Name)
	require.Equal(t, "ping", planner.funcs["ping-ping"].scheme)
	require.Equal(t, "ping", planner.funcs["ping-ping"].action.Name)
	require.Len(t, planner.funcs, 2)
}

func Test_Opener_resolves_api_key(t *testing.T) {
	testCases := map[string]struct {
		url        string
		creds      cred.Store
		wantAPIKey string
		wantErr    string
	}{
		"present": {
			url:        "openai-chat-completions://host/v1?model=m&cred-prefix=openai",
			creds:      fakeCreds{values: map[string]string{"openai-api-key": "sk-1"}},
			wantAPIKey: "sk-1",
		},
		"missing-is-not-error": {
			url:   "openai-chat-completions://host/v1?model=m&cred-prefix=openai",
			creds: fakeCreds{values: map[string]string{}},
		},
		"no-prefix-skips-lookup": {
			url:   "openai-chat-completions://host/v1?model=m",
			creds: fakeCreds{values: map[string]string{"openai-api-key": "sk-1"}},
		},
		"nil-store": {
			url: "openai-chat-completions://host/v1?model=m&cred-prefix=openai", creds: nil,
		},
		"cred-prefix-selects-key": {
			url:        "openai-chat-completions://host/v1?model=m&cred-prefix=gemini",
			creds:      fakeCreds{values: map[string]string{"gemini-api-key": "g-1"}},
			wantAPIKey: "g-1",
		},
		"store-error": {
			url:     "openai-chat-completions://host/v1?model=m&cred-prefix=openai",
			creds:   fakeCreds{err: errors.New("boom")},
			wantErr: "resolve API key",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			opened, err := openerViaRegistry(t, tc.url, tc.creds, nil)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			defer func() { require.NoError(t, opened.Close()) }()
			require.Equal(t, tc.wantAPIKey, opened.(*Planner).apiKey)
		})
	}
}

// plannerAgainst opens a planner pointed at server, with the given tools
// and credentials, using the insecure local endpoint form.
func plannerAgainst(t *testing.T, server *httptest.Server, creds cred.Store, tools *tool.Registry) planner.Planner {
	t.Helper()
	host := strings.TrimPrefix(server.URL, "http://")
	rawURL := "openai-chat-completions://" + host + "/v1?insecure=true&model=test-model&cred-prefix=openai"
	opened, err := openerViaRegistry(t, rawURL, creds, tools)
	require.NoError(t, err)
	return opened
}

func Test_Plan_maps_completion_to_steps(t *testing.T) {
	var gotBody chatRequest
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"checking now",`+
			`"tool_calls":[{"type":"function","function":{"name":"status-check","arguments":"{\"target\":\"github\"}"}}]}}]}`)
	}))
	defer server.Close()

	statusDesc := &tool.Descriptor{Summary: "status", Actions: []tool.Action{{Name: "check", TakesTarget: true, TargetDesc: "the service"}}}
	tools := tool.NewRegistry(tool.Backend{Scheme: "status", Opener: fakeToolOpener, Descriptor: statusDesc})
	creds := fakeCreds{values: map[string]string{"openai-api-key": "sk-test"}}
	p := plannerAgainst(t, server, creds, tools)
	defer func() { require.NoError(t, p.Close()) }()

	plan, err := p.Plan(context.Background(), planner.Request{Text: "is github up?", ConversationID: "C1"})
	require.NoError(t, err)

	require.Equal(t, []planner.Step{
		{Tool: reply.URL, Call: tool.Call{Action: "send", Target: "C1", Parameters: map[string]string{"text": "checking now"}}},
		{Tool: "status://", Call: tool.Call{Action: "check", Target: "github"}},
	}, plan.Steps)

	// The request carried the model, the enabled tool functions, and the key.
	require.Equal(t, "test-model", gotBody.Model)
	require.Equal(t, "Bearer sk-test", gotAuth)
	names := make([]string, len(gotBody.Tools))
	for i, def := range gotBody.Tools {
		names[i] = def.Function.Name
	}
	require.Equal(t, []string{"reply", "status-check"}, names)
	require.Len(t, gotBody.Messages, 2)
	require.Equal(t, "system", gotBody.Messages[0].Role)
	require.Equal(t, "is github up?", gotBody.Messages[1].Content)
}

func Test_Plan_reports_no_choices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer server.Close()

	p := plannerAgainst(t, server, nil, nil)
	defer func() { require.NoError(t, p.Close()) }()
	_, err := p.Plan(context.Background(), planner.Request{Text: "hi"})
	require.ErrorContains(t, err, "no choices")
}

func Test_Plan_propagates_request_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := plannerAgainst(t, server, nil, nil)
	defer func() { require.NoError(t, p.Close()) }()
	_, err := p.Plan(context.Background(), planner.Request{Text: "hi"})
	require.ErrorContains(t, err, "HTTP 500")
}

func Test_Plan_honors_cancelled_context(t *testing.T) {
	p := plannerAgainst(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), nil, nil)
	defer func() { require.NoError(t, p.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Plan(ctx, planner.Request{Text: "hi"})
	require.Error(t, err)
}

func Test_Open_validates_config(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	testCases := map[string]struct {
		ctx    context.Context
		cfg    Config
		errMsg string
	}{
		"empty-base-url":      {ctx: context.Background(), cfg: Config{Model: "m"}, errMsg: "base URL"},
		"unparseable-base":    {ctx: context.Background(), cfg: Config{BaseURL: "://bad", Model: "m"}, errMsg: "parse base URL"},
		"non-http-base":       {ctx: context.Background(), cfg: Config{BaseURL: "ftp://x/v1", Model: "m"}, errMsg: "must use http or https"},
		"hostless-base":       {ctx: context.Background(), cfg: Config{BaseURL: "https:///v1", Model: "m"}, errMsg: "has no host"},
		"query-in-base":       {ctx: context.Background(), cfg: Config{BaseURL: "https://x/v1?a=b", Model: "m"}, errMsg: "must not carry query"},
		"empty-model":         {ctx: context.Background(), cfg: Config{BaseURL: "https://x"}, errMsg: "model"},
		"cancelled-ctx":       {ctx: cancelled, cfg: Config{BaseURL: "https://x", Model: "m"}, errMsg: "context canceled"},
		"invalid-tool-scheme": {ctx: context.Background(), cfg: Config{BaseURL: "https://x", Model: "m", ToolSchemes: []string{"bad.name"}}, errMsg: "OpenAI function name"},
		"missing-descriptor":  {ctx: context.Background(), cfg: Config{BaseURL: "https://x", Model: "m", ToolSchemes: []string{"ping"}}, errMsg: `enabled tool "ping" has no descriptor`},
		"invalid-descriptor": {ctx: context.Background(), cfg: Config{
			BaseURL: "https://x", Model: "m", ToolSchemes: []string{"ping"},
			ToolDescriptors: map[string]tool.Descriptor{"ping": {Summary: "x"}}, // no actions
		}, errMsg: "invalid"},
		"bad-action-name": {ctx: context.Background(), cfg: Config{
			BaseURL: "https://x", Model: "m", ToolSchemes: []string{"ping"},
			// A valid descriptor whose action name yields an invalid
			// function name is rejected while building the catalog.
			ToolDescriptors: map[string]tool.Descriptor{"ping": {Summary: "x", Actions: []tool.Action{{Name: "do it"}}}},
		}, errMsg: "invalid function name"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := Open(tc.ctx, tc.cfg)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Open_normalizes_base_url(t *testing.T) {
	testCases := map[string]string{
		"trailing-slash": "https://x/v1/",
		"double-slash":   "https://x/v1//",
		"clean":          "https://x/v1",
	}
	for name, raw := range testCases {
		t.Run(name, func(t *testing.T) {
			p, err := Open(context.Background(), Config{BaseURL: raw, Model: "m"})
			require.NoError(t, err)
			defer func() { require.NoError(t, p.Close()) }()
			require.Equal(t, "https://x/v1", p.baseURL)
		})
	}
}

func Test_Open_preserves_escaped_base_path(t *testing.T) {
	// An encoded "?" must stay in the path rather than decoding into a
	// query string on rebuild.
	testCases := map[string]string{
		"encoded-question": "https://host/v1%3Ftenant",
		"encoded-slash":    "https://host/a%2Fb",
	}
	for name, raw := range testCases {
		t.Run(name, func(t *testing.T) {
			p, err := Open(context.Background(), Config{BaseURL: raw, Model: "m"})
			require.NoError(t, err)
			defer func() { require.NoError(t, p.Close()) }()
			require.Equal(t, raw, p.baseURL)
		})
	}
}

func Test_Open_rejects_scheme_without_descriptor(t *testing.T) {
	_, err := Open(context.Background(), Config{
		BaseURL: "https://x", Model: "m", ToolSchemes: []string{"ping"},
	})
	require.ErrorContains(t, err, `enabled tool "ping" has no descriptor`)
}

// functionParams returns the JSON Schema offered for the named function.
func functionParams(defs []toolDef, name string) json.RawMessage {
	for _, d := range defs {
		if d.Function.Name == name {
			return d.Function.Parameters
		}
	}
	return nil
}

func Test_Open_snapshots_tool_config(t *testing.T) {
	schemes := []string{"ping"}
	descriptors := map[string]tool.Descriptor{
		"ping": {Summary: "liveness", Actions: []tool.Action{
			{Name: "ping", Parameters: []tool.Param{{Name: "loud", Type: "boolean"}}},
		}},
	}
	p, err := Open(context.Background(), Config{
		BaseURL: "https://x", Model: "m", ToolSchemes: schemes, ToolDescriptors: descriptors,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, p.Close()) }()

	require.Equal(t, "ping", p.funcs["ping-ping"].scheme)
	require.Equal(t, "ping", p.funcs["ping-ping"].action.Name)
	before := string(functionParams(p.defs, "ping-ping"))
	require.Contains(t, before, "loud")

	// Mutating the caller's scheme slice and the descriptor's nested Actions
	// and Parameters after Open must not change the offered catalog: Open
	// deep-copies the descriptors and the schemas are already serialized.
	schemes[0] = "mutated"
	acts := descriptors["ping"].Actions
	acts[0].Name = "changed"
	acts[0].Parameters[0].Name = "changed"
	delete(descriptors, "ping")

	require.Contains(t, p.funcs, "ping-ping")
	require.NotContains(t, p.funcs, "mutated-ping")
	require.JSONEq(t, before, string(functionParams(p.defs, "ping-ping")))
	require.NotContains(t, string(functionParams(p.defs, "ping-ping")), "changed")
}
