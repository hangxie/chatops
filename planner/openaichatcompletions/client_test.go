package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

// completionJSON is a minimal well-formed Chat Completions response.
const completionJSON = `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`

func Test_chatComplete_sends_request_and_decodes(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	var gotBody chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_, _ = io.WriteString(w, completionJSON)
	}))
	defer server.Close()

	req := chatRequest{Model: "gpt-5", Messages: []reqMessage{{Role: "user", Content: "hi"}}}
	resp, err := chatComplete(context.Background(), server.Client(), server.URL, "secret-key", req)
	require.NoError(t, err)

	require.Equal(t, "/chat/completions", gotPath)
	require.Equal(t, "Bearer secret-key", gotAuth)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, "gpt-5", gotBody.Model)
	require.Len(t, resp.Choices, 1)
	require.Equal(t, "hi", resp.Choices[0].Message.Content)
}

func Test_chatComplete_omits_authorization_without_key(t *testing.T) {
	var hasAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasAuth = r.Header["Authorization"]
		_, _ = io.WriteString(w, completionJSON)
	}))
	defer server.Close()

	_, err := chatComplete(context.Background(), server.Client(), server.URL, "", chatRequest{Model: "m"})
	require.NoError(t, err)
	require.False(t, hasAuth)
}

func Test_chatComplete_reports_http_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad key"}`)
	}))
	defer server.Close()

	_, err := chatComplete(context.Background(), server.Client(), server.URL, "k", chatRequest{Model: "m"})
	require.ErrorContains(t, err, "HTTP 401")
	require.ErrorContains(t, err, "bad key")
}

func Test_chatComplete_reports_malformed_json(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "{not json")
	}))
	defer server.Close()

	_, err := chatComplete(context.Background(), server.Client(), server.URL, "k", chatRequest{Model: "m"})
	require.ErrorContains(t, err, "decode completion response")
}

func Test_chatComplete_reports_transport_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening now

	_, err := chatComplete(context.Background(), http.DefaultClient, url, "k", chatRequest{Model: "m"})
	require.ErrorContains(t, err, "request completion")
}

func Test_chatComplete_reports_encode_error(t *testing.T) {
	// An invalid json.RawMessage in a tool schema makes the request body
	// unmarshalable, exercising the encode-request error path.
	req := chatRequest{Model: "m", Tools: []toolDef{{
		Type:     "function",
		Function: functionDef{Name: "x", Parameters: json.RawMessage("{not json")},
	}}}
	_, err := chatComplete(context.Background(), http.DefaultClient, "http://unused", "k", req)
	require.ErrorContains(t, err, "encode request")
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// errCloseBody yields body then returns an error from Close.
type errCloseBody struct {
	reader   io.Reader
	closeErr error
}

func (b *errCloseBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *errCloseBody) Close() error               { return b.closeErr }

func Test_chatComplete_joins_body_close_error(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       &errCloseBody{reader: strings.NewReader(completionJSON), closeErr: errors.New("close boom")},
		}, nil
	})}

	_, err := chatComplete(context.Background(), client, "http://unused", "k", chatRequest{Model: "m"})
	require.ErrorContains(t, err, "close completion response")
	require.ErrorContains(t, err, "close boom")
}

func Test_chatComplete_reports_body_read_error(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(iotest.ErrReader(errors.New("read boom"))),
		}, nil
	})}
	_, err := chatComplete(context.Background(), client, "http://unused", "k", chatRequest{Model: "m"})
	require.ErrorContains(t, err, "read completion response")
}

func Test_chatComplete_rejects_oversized_response(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A body one byte over the cap: enough to trip the limit without
		// depending on the payload being valid JSON.
		_, _ = w.Write(make([]byte, maxResponseBytes+1))
	}))
	defer server.Close()

	_, err := chatComplete(context.Background(), server.Client(), server.URL, "k", chatRequest{Model: "m"})
	require.ErrorContains(t, err, "exceeds")
}

func Test_chatComplete_reports_request_build_error(t *testing.T) {
	// A control character in the URL makes http.NewRequestWithContext
	// fail, exercising the create-request error path.
	_, err := chatComplete(context.Background(), http.DefaultClient, "http://bad\x7fhost", "k", chatRequest{Model: "m"})
	require.ErrorContains(t, err, "create request")
}

func Test_chatComplete_honors_context_cancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, completionJSON)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := chatComplete(ctx, server.Client(), server.URL, "k", chatRequest{Model: "m"})
	require.Error(t, err)
}
