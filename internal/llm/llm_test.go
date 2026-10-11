package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
)

const textResponse = `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`

func Test_New(t *testing.T) {
	t.Setenv("CHATOPS_TEST_LLM_KEY", "k")

	tests := map[string]struct {
		cfg     config.LLM
		baseURL string
		apiKey  string
		errMsg  string
	}{
		"keyless":        {cfg: config.LLM{BaseURL: "http://h:8080/v1/", Model: "m"}, baseURL: "http://h:8080/v1"},
		"with-key":       {cfg: config.LLM{BaseURL: "https://h/v1", Model: "m", APIKeyEnv: "CHATOPS_TEST_LLM_KEY"}, baseURL: "https://h/v1", apiKey: "k"},
		"missing-key":    {cfg: config.LLM{BaseURL: "https://h/v1", Model: "m", APIKeyEnv: "CHATOPS_TEST_LLM_MISSING"}, errMsg: "llm api key: environment variable CHATOPS_TEST_LLM_MISSING is not set"},
		"query":          {cfg: config.LLM{BaseURL: "https://h/v1?x=1", Model: "m"}, errMsg: `llm base URL "https://h/v1?x=1" must not carry query, fragment, or userinfo`},
		"userinfo":       {cfg: config.LLM{BaseURL: "https://u:p@h/v1", Model: "m"}, errMsg: "must not carry query, fragment, or userinfo"},
		"bad-scheme":     {cfg: config.LLM{BaseURL: "ftp://h/v1", Model: "m"}, errMsg: `llm base URL "ftp://h/v1" must use http or https`},
		"no-host":        {cfg: config.LLM{BaseURL: "http:///v1", Model: "m"}, errMsg: `llm base URL "http:///v1" has no host`},
		"unparseable":    {cfg: config.LLM{BaseURL: "http://h/%zz", Model: "m"}, errMsg: "parse llm base URL"},
		"trailing-slash": {cfg: config.LLM{BaseURL: "http://h/", Model: "m"}, baseURL: "http://h"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client, err := New(tc.cfg, nil)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.baseURL, client.baseURL)
			require.Equal(t, tc.apiKey, client.apiKey)
			require.NotNil(t, client.httpClient)
		})
	}
}

func Test_Client_Complete(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		_, _ = io.WriteString(w, textResponse)
	}))
	defer server.Close()

	t.Setenv("CHATOPS_TEST_LLM_KEY", "k")
	client, err := New(config.LLM{BaseURL: server.URL + "/v1", Model: "m", APIKeyEnv: "CHATOPS_TEST_LLM_KEY", DisableThinking: true}, server.Client())
	require.NoError(t, err)

	resp, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	require.NoError(t, err)
	require.Equal(t, Response{Message: Message{Role: RoleAssistant, Content: "hi"}}, resp)
	require.Equal(t, "/v1/chat/completions", gotPath)
	require.Equal(t, "Bearer k", gotAuth)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, "m", gotBody["model"])
	require.Equal(t, "none", gotBody["reasoning_effort"])
}

func Test_Client_Complete_sends_sampling(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		_, _ = io.WriteString(w, textResponse)
	}))
	defer server.Close()

	temp := 0.0
	client, err := New(config.LLM{BaseURL: server.URL, Model: "m", Sampling: config.Sampling{Temperature: &temp}}, server.Client())
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.NoError(t, err)
	require.Equal(t, float64(0), gotBody["temperature"])

	// A client without sampling must omit the field entirely.
	gotBody = nil
	plain, err := New(config.LLM{BaseURL: server.URL, Model: "m"}, server.Client())
	require.NoError(t, err)
	_, err = plain.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.NoError(t, err)
	_, ok := gotBody["temperature"]
	require.False(t, ok)
}

func Test_Client_Complete_keyless_sends_no_auth(t *testing.T) {
	sawAuth := "unset"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, textResponse)
	}))
	defer server.Close()

	client, err := New(config.LLM{BaseURL: server.URL, Model: "m"}, server.Client())
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	require.NoError(t, err)
	require.Empty(t, sawAuth)
}

func Test_Client_Complete_authenticated_refuses_redirect(t *testing.T) {
	tests := map[string]func(http.Handler) *httptest.Server{
		"cross-origin":  httptest.NewServer,
		"https-to-http": httptest.NewTLSServer,
	}

	for name, newRedirector := range tests {
		t.Run(name, func(t *testing.T) {
			targetRequests := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetRequests++
				_, _ = io.WriteString(w, textResponse)
			}))
			defer target.Close()
			redirector := newRedirector(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
			}))
			defer redirector.Close()

			t.Setenv("CHATOPS_TEST_LLM_KEY", "k")
			httpClient := redirector.Client()
			client, err := New(config.LLM{BaseURL: redirector.URL, Model: "m", APIKeyEnv: "CHATOPS_TEST_LLM_KEY"}, httpClient)
			require.NoError(t, err)
			require.Nil(t, httpClient.CheckRedirect, "caller's client must not be modified")

			_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
			require.ErrorContains(t, err, "refusing to follow redirect")
			require.Zero(t, targetRequests, "redirect target must not be contacted")
		})
	}
}

func Test_Client_Complete_keyless_follows_redirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, textResponse)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client, err := New(config.LLM{BaseURL: redirector.URL, Model: "m"}, nil)
	require.NoError(t, err)
	resp, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.NoError(t, err)
	require.Equal(t, "hi", resp.Message.Content)
}

func Test_Client_Complete_errors(t *testing.T) {
	tests := map[string]struct {
		handler http.HandlerFunc
		errMsg  string
	}{
		"http-status": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "model not loaded", http.StatusServiceUnavailable)
			},
			errMsg: "llm: completion HTTP 503 Service Unavailable: model not loaded",
		},
		"oversized": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+strings.Repeat("a", maxResponseBytes)+`"}}]}`)
			},
			errMsg: "llm: completion response exceeds 1048576 bytes",
		},
		"not-json": {
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") },
			errMsg:  "llm: decode completion response",
		},
		"no-choices": {
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"choices":[]}`) },
			errMsg:  "llm: response has no choices",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			client, err := New(config.LLM{BaseURL: server.URL, Model: "m"}, server.Client())
			require.NoError(t, err)

			_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Client_Complete_unreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	client, err := New(config.LLM{BaseURL: url, Model: "m"}, nil)
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), Request{})
	require.ErrorContains(t, err, "llm: request completion")
}

func Test_Client_Complete_cancelled(t *testing.T) {
	client, err := New(config.LLM{BaseURL: "http://127.0.0.1:1", Model: "m"}, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Complete(ctx, Request{})
	require.ErrorIs(t, err, context.Canceled)
}
