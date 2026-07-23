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
	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

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
			url: "openai-chat-completions://api.openai.com?model=gpt-5&keyless=true", wantBaseURL: "https://api.openai.com/v1", wantModel: "gpt-5",
		},
		"host-and-path": {
			url:         "openai-chat-completions://generativelanguage.googleapis.com/v1beta/openai?model=gemini-3.1-flash-lite&keyless=true",
			wantBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
			wantModel:   "gemini-3.1-flash-lite",
		},
		"insecure-local": {
			url: "openai-chat-completions://localhost:11434/v1?insecure=true&model=llama3&keyless=true", wantBaseURL: "http://localhost:11434/v1", wantModel: "llama3",
		},
		"trailing-slash-trimmed": {
			url: "openai-chat-completions://host/v1/?model=m&keyless=true", wantBaseURL: "https://host/v1", wantModel: "m",
		},
		"encoded-path-preserved": {
			url: "openai-chat-completions://host/v1%3Ftenant?model=m&keyless=true", wantBaseURL: "https://host/v1%3Ftenant", wantModel: "m",
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
		"cred-prefix":   {url: "openai-chat-completions://host?model=m&cred-prefix=openai", errMsg: `unknown query parameter "cred-prefix"`},
		"bad-keyless":   {url: "openai-chat-completions://host?model=m&keyless=yes", errMsg: "keyless must be true or false"},
		"bad-insecure":  {url: "openai-chat-completions://host?model=m&insecure=yes", errMsg: "insecure must be true or false"},
		"duplicate-model": {
			url: "openai-chat-completions://host?model=a&model=b", errMsg: `query parameter "model" must appear once`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := openerViaRegistry(t, tc.url, nil, nil)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Opener_offers_enabled_tools(t *testing.T) {
	statusDesc := tool.Descriptor{Description: "status list"}
	pingDesc := tool.Descriptor{Description: "ping"}
	tools := tool.NewRegistry(
		tool.Backend{Scheme: "status-list", Opener: fakeToolOpener, Descriptor: &statusDesc},
		tool.Backend{Scheme: "ping", Opener: fakeToolOpener, Descriptor: &pingDesc},
	)
	opened, err := openerViaRegistry(t, "openai-chat-completions://host/v1?model=m&keyless=true", nil, tools)
	require.NoError(t, err)
	defer func() { require.NoError(t, opened.Close()) }()
	planner := opened.(*Planner)
	// Every enabled tool becomes an offered function keyed back to its scheme.
	require.Equal(t, "status-list", planner.funcs["status-list"].scheme)
	require.Equal(t, "ping", planner.funcs["ping"].scheme)
	require.Len(t, planner.funcs, 2)
}

func Test_Opener_resolves_api_key(t *testing.T) {
	testCases := map[string]struct {
		url        string
		creds      cred.Store
		wantAPIKey string
		wantErr    string
		wantErrIs  error
	}{
		"present": {
			url:        "openai-chat-completions://host/v1?model=m&keyless=false",
			creds:      testutils.CredentialStore{Values: map[cred.Key]string{cred.PlannerAPIKey: "sk-1"}},
			wantAPIKey: "sk-1",
		},
		"missing": {
			url:       "openai-chat-completions://host/v1?model=m",
			creds:     testutils.CredentialStore{},
			wantErr:   "planner.api-key",
			wantErrIs: cred.ErrNotFound,
		},
		"explicit-keyless-skips-lookup": {
			url:   "openai-chat-completions://host/v1?model=m&keyless=true",
			creds: testutils.CredentialStore{Err: errors.New("must not be called")},
		},
		"nil-store": {
			url: "openai-chat-completions://host/v1?model=m", creds: nil,
			wantErr:   "credential store is not configured; use keyless=true for an unauthenticated endpoint",
			wantErrIs: cred.ErrStoreNotConfigured,
		},
		"empty": {
			url:     "openai-chat-completions://host/v1?model=m",
			creds:   testutils.CredentialStore{Values: map[cred.Key]string{cred.PlannerAPIKey: ""}},
			wantErr: "planner.api-key is empty",
		},
		"store-error": {
			url:     "openai-chat-completions://host/v1?model=m",
			creds:   testutils.CredentialStore{Err: errors.New("boom")},
			wantErr: "resolve planner.api-key",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			opened, err := openerViaRegistry(t, tc.url, tc.creds, nil)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				if tc.wantErrIs != nil {
					require.ErrorIs(t, err, tc.wantErrIs)
				}
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
	rawURL := "openai-chat-completions://" + host + "/v1?insecure=true&model=test-model"
	if creds == nil {
		rawURL += "&keyless=true"
	}
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
			`"tool_calls":[{"type":"function","function":{"name":"status-check","arguments":"{\"service\":\"github\"}"}}]}}]}`)
	}))
	defer server.Close()

	statusDesc := &tool.Descriptor{Description: "check one service", Parameters: []tool.Param{{Name: "service", Type: "string", Required: true, Description: "the service"}}}
	tools := tool.NewRegistry(tool.Backend{Scheme: "status-check", Opener: fakeToolOpener, Descriptor: statusDesc})
	creds := testutils.CredentialStore{Values: map[cred.Key]string{cred.PlannerAPIKey: "sk-test"}}
	p := plannerAgainst(t, server, creds, tools)
	defer func() { require.NoError(t, p.Close()) }()

	plan, err := p.Plan(context.Background(), planner.Request{Text: "is github up?", ConversationID: "C1"})
	require.NoError(t, err)

	// The reply step carries only the text; the executor injects the target
	// conversation.
	require.Equal(t, []planner.Step{
		{Tool: reply.URL, Call: tool.Call{Arguments: map[string]string{"text": "checking now"}}},
		{Tool: "status-check://", Call: tool.Call{Arguments: map[string]string{"service": "github"}}},
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
			ToolDescriptors: map[string]tool.Descriptor{"ping": {}}, // no description
		}, errMsg: "invalid"},
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
		"ping": {Description: "liveness", Parameters: []tool.Param{{Name: "loud", Type: "boolean"}}},
	}
	p, err := Open(context.Background(), Config{
		BaseURL: "https://x", Model: "m", ToolSchemes: schemes, ToolDescriptors: descriptors,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, p.Close()) }()

	require.Equal(t, "ping", p.funcs["ping"].scheme)
	before := string(functionParams(p.defs, "ping"))
	require.Contains(t, before, "loud")

	// Mutating the caller's scheme slice and the descriptor's nested
	// Parameters after Open must not change the offered catalog: Open
	// deep-copies the descriptors and the schemas are already serialized.
	schemes[0] = "mutated"
	params := descriptors["ping"].Parameters
	params[0].Name = "changed"
	delete(descriptors, "ping")

	require.Contains(t, p.funcs, "ping")
	require.NotContains(t, p.funcs, "mutated")
	require.JSONEq(t, before, string(functionParams(p.defs, "ping")))
	require.NotContains(t, string(functionParams(p.defs, "ping")), "changed")
}
