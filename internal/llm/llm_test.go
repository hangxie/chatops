package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
)

func streamOff() *bool {
	off := false
	return &off
}

const textResponse = `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`

func Test_New(t *testing.T) {
	t.Setenv("CHATOPS_TEST_LLM_KEY", "k")

	tests := map[string]struct {
		cfg     config.Model
		baseURL string
		apiKey  string
		errMsg  string
	}{
		"keyless":        {cfg: config.Model{BaseURL: "http://h:8080/v1/", Model: "m"}, baseURL: "http://h:8080/v1"},
		"with-key":       {cfg: config.Model{BaseURL: "https://h/v1", Model: "m", APIKeyEnv: "CHATOPS_TEST_LLM_KEY"}, baseURL: "https://h/v1", apiKey: "k"},
		"missing-key":    {cfg: config.Model{BaseURL: "https://h/v1", Model: "m", APIKeyEnv: "CHATOPS_TEST_LLM_MISSING"}, errMsg: "llm api key: environment variable CHATOPS_TEST_LLM_MISSING is not set"},
		"query":          {cfg: config.Model{BaseURL: "https://h/v1?x=1", Model: "m"}, errMsg: `llm base URL "https://h/v1?x=1" must not carry query, fragment, or userinfo`},
		"userinfo":       {cfg: config.Model{BaseURL: "https://u:p@h/v1", Model: "m"}, errMsg: "must not carry query, fragment, or userinfo"},
		"bad-scheme":     {cfg: config.Model{BaseURL: "ftp://h/v1", Model: "m"}, errMsg: `llm base URL "ftp://h/v1" must use http or https`},
		"no-host":        {cfg: config.Model{BaseURL: "http:///v1", Model: "m"}, errMsg: `llm base URL "http:///v1" has no host`},
		"unparseable":    {cfg: config.Model{BaseURL: "http://h/%zz", Model: "m"}, errMsg: "parse llm base URL"},
		"trailing-slash": {cfg: config.Model{BaseURL: "http://h/", Model: "m"}, baseURL: "http://h"},
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
	var gotPath, gotAuth, gotContentType, gotAccept string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`)
	}))
	defer server.Close()

	t.Setenv("CHATOPS_TEST_LLM_KEY", "k")
	client, err := New(config.Model{BaseURL: server.URL + "/v1", Model: "m", APIKeyEnv: "CHATOPS_TEST_LLM_KEY", DisableThinking: true, Stream: streamOff()}, server.Client())
	require.NoError(t, err)

	resp, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	require.NoError(t, err)
	require.Equal(t, Response{Message: Message{Role: RoleAssistant, Content: "hi"}, Usage: &Usage{PromptTokens: 7, CompletionTokens: 2}, Model: "m"}, resp)
	require.Equal(t, "/v1/chat/completions", gotPath)
	require.Equal(t, "Bearer k", gotAuth)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, "application/json", gotAccept)
	require.Equal(t, "m", gotBody["model"])
	require.Equal(t, "none", gotBody["reasoning_effort"])
	_, streamed := gotBody["stream"]
	require.False(t, streamed)
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
	client, err := New(config.Model{BaseURL: server.URL, Model: "m", Sampling: config.Sampling{Temperature: &temp}}, server.Client())
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.NoError(t, err)
	require.Equal(t, float64(0), gotBody["temperature"])

	// A client without sampling must omit the field entirely.
	gotBody = nil
	plain, err := New(config.Model{BaseURL: server.URL, Model: "m"}, server.Client())
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

	client, err := New(config.Model{BaseURL: server.URL, Model: "m"}, server.Client())
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
			client, err := New(config.Model{BaseURL: redirector.URL, Model: "m", APIKeyEnv: "CHATOPS_TEST_LLM_KEY"}, httpClient)
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

	client, err := New(config.Model{BaseURL: redirector.URL, Model: "m"}, nil)
	require.NoError(t, err)
	resp, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.NoError(t, err)
	require.Equal(t, "hi", resp.Message.Content)
}

func Test_Client_Complete_errors(t *testing.T) {
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "model not loaded", code) }
	}
	tests := map[string]struct {
		handler     http.HandlerFunc
		errMsg      string
		unavailable bool
	}{
		"http-503":   {handler: status(http.StatusServiceUnavailable), errMsg: "llm: completion HTTP 503 Service Unavailable: model not loaded", unavailable: true},
		"http-500":   {handler: status(http.StatusInternalServerError), errMsg: "llm: completion HTTP 500", unavailable: true},
		"http-429":   {handler: status(http.StatusTooManyRequests), errMsg: "llm: completion HTTP 429", unavailable: true},
		"http-400":   {handler: status(http.StatusBadRequest), errMsg: "llm: completion HTTP 400"},
		"http-401":   {handler: status(http.StatusUnauthorized), errMsg: "llm: completion HTTP 401"},
		"http-404":   {handler: status(http.StatusNotFound), errMsg: "llm: completion HTTP 404"},
		"length":     {handler: reply(`{"choices":[{"message":{"content":"a"},"finish_reason":"length"}]}`), errMsg: "llm: response truncated"},
		"oversized":  {handler: reply(`{"choices":[{"message":{"content":"` + strings.Repeat("a", maxResponseBytes) + `"}}]}`), errMsg: "llm: completion response exceeds 1048576 bytes"},
		"not-json":   {handler: reply("<html>"), errMsg: "llm: decode completion response"},
		"no-choices": {handler: reply(`{"choices":[]}`), errMsg: "llm: response has no choices"},
		"bad-stream": {handler: streamReply("data: {\"error\":{\"message\":\"boom\"}}\n\n"), errMsg: "llm: stream error: boom"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			client, err := New(config.Model{BaseURL: server.URL, Model: "m"}, server.Client())
			require.NoError(t, err)

			_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
			require.ErrorContains(t, err, tc.errMsg)
			require.Equal(t, tc.unavailable, errors.Is(err, ErrUnavailable))
		})
	}
}

func Test_Client_Complete_retry_http_codes(t *testing.T) {
	tests := map[string]struct {
		codes       []string
		status      int
		unavailable bool
	}{
		"default-401":   {codes: nil, status: http.StatusUnauthorized},
		"listed-401":    {codes: []string{"401", "403"}, status: http.StatusUnauthorized, unavailable: true},
		"unlisted-503":  {codes: []string{"401", "403"}, status: http.StatusServiceUnavailable},
		"none-503":      {codes: []string{}, status: http.StatusServiceUnavailable},
		"class-5xx-502": {codes: []string{"5xx"}, status: http.StatusBadGateway, unavailable: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "nope", tc.status)
			}))
			defer server.Close()
			client, err := New(config.Model{BaseURL: server.URL, Model: "m", RetryHTTPCodes: tc.codes}, server.Client())
			require.NoError(t, err)

			_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
			require.ErrorContains(t, err, fmt.Sprintf("llm: completion HTTP %d", tc.status))
			require.Equal(t, tc.unavailable, errors.Is(err, ErrUnavailable))
		})
	}
}

func reply(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }
}

func streamReply(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
}

func Test_Client_Complete_unreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	client, err := New(config.Model{BaseURL: url, Model: "m"}, nil)
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), Request{})
	require.ErrorContains(t, err, "llm: request completion")
	require.ErrorIs(t, err, ErrUnavailable)
}

func Test_Client_Complete_cancelled(t *testing.T) {
	client, err := New(config.Model{BaseURL: "http://127.0.0.1:1", Model: "m"}, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Complete(ctx, Request{})
	require.ErrorIs(t, err, context.Canceled)
	// The caller gave up, so a backup must not be tried.
	require.NotErrorIs(t, err, ErrUnavailable)
}

func Test_Client_Complete_connection_dropped(t *testing.T) {
	tests := map[string]struct {
		contentType string
		partial     string
	}{
		"json":   {contentType: "application/json", partial: `{"choices":[{"message":`},
		"stream": {contentType: "text/event-stream", partial: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.partial)
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}))
			defer server.Close()
			client, err := New(config.Model{BaseURL: server.URL, Model: "m"}, server.Client())
			require.NoError(t, err)

			_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			require.ErrorIs(t, err, ErrUnavailable)
		})
	}
}

func Test_Client_Complete_names_model(t *testing.T) {
	server := httptest.NewServer(reply(textResponse))
	defer server.Close()
	client, err := New(config.Model{BaseURL: server.URL, Model: "m", Name: "primary"}, server.Client())
	require.NoError(t, err)
	resp, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.NoError(t, err)
	require.Equal(t, "primary", resp.Model)
}

func Test_Client_Complete_model_timeout(t *testing.T) {
	hang := func(stream bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// The server sees the client leave only once the body is read.
			_, _ = io.ReadAll(r.Body)
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"thinking\"}}]}\n\n")
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		}
	}
	tests := map[string]struct {
		handler http.HandlerFunc
	}{
		"before-headers": {handler: hang(false)},
		"mid-stream":     {handler: hang(true)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			client, err := New(config.Model{BaseURL: server.URL, Model: "m", Timeout: 50 * time.Millisecond}, server.Client())
			require.NoError(t, err)

			_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
			require.ErrorContains(t, err, "model call exceeded 50ms")
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.ErrorIs(t, err, ErrUnavailable)
		})
	}
}

func Test_Client_Complete_stream(t *testing.T) {
	var gotAccept string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client, err := New(config.Model{BaseURL: server.URL, Model: "m"}, server.Client())
	require.NoError(t, err)
	resp, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	require.NoError(t, err)
	require.Equal(t, Response{Message: Message{Role: RoleAssistant, Content: "hi"}, Usage: &Usage{PromptTokens: 7, CompletionTokens: 2}, Model: "m"}, resp)
	require.Equal(t, "text/event-stream, application/json", gotAccept)
	require.Equal(t, true, gotBody["stream"])
	require.Equal(t, map[string]any{"include_usage": true}, gotBody["stream_options"])
}

func Test_Client_Complete_stream_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"boom\"}}\n\n")
	}))
	defer server.Close()

	client, err := New(config.Model{BaseURL: server.URL, Model: "m"}, server.Client())
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.EqualError(t, err, "llm: stream error: boom")
}

// A proxy only cancels upstream generation when it sees the client go away mid-stream.
func Test_Client_Complete_deadline_reaches_server(t *testing.T) {
	serverSawCancel := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"thinking\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(serverSawCancel)
	}))
	defer server.Close()

	client, err := New(config.Model{BaseURL: server.URL, Model: "m"}, server.Client())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = client.Complete(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-serverSawCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("server never saw the request cancelled")
	}
}
