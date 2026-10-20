// Package llm is a small client for OpenAI-compatible Chat Completions endpoints.
package llm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hangxie/chatops/internal/config"
)

// Role identifies a message author.
type Role string

// Message roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry of a conversation sent to or received from the model.
type Message struct {
	Role    Role
	Content string
	// ToolCalls is set on assistant messages that request tool execution.
	ToolCalls []ToolCall
	// ToolCallID links a tool-result message to the call it answers.
	ToolCallID string
}

// ToolCall is a model's request to run a tool; Arguments is raw, possibly invalid JSON.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolSpec describes a tool offered to the model.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolChoiceRequired makes the model call at least one tool instead of answering in text.
const ToolChoiceRequired = "required"

// Request is one completion request.
type Request struct {
	Messages []Message
	Tools    []ToolSpec
	// ToolChoice is empty for the endpoint default (auto) or ToolChoiceRequired; it is sent only with tools.
	ToolChoice string
}

// Response holds the assistant message: final text, tool calls, or both.
type Response struct {
	Message Message
	// Usage is nil when the endpoint does not report token counts.
	Usage *Usage
	// Model names the configured model that answered, or the last one tried when a call fails.
	Model string
	// Attempts lists every model a pool tried for this call, in order; the last one is Model.
	Attempts []Attempt
}

// Attempt is one model's try at a call.
type Attempt struct {
	Model    string
	Duration time.Duration
	// Err is nil for the attempt that answered.
	Err error
}

// ErrUnavailable marks failures another endpoint may not share: connection errors, the model's retry_http_codes, and its timeout.
var ErrUnavailable = errors.New("model unavailable")

type unavailableError struct{ error }

func (e unavailableError) Unwrap() []error { return []error{e.error, ErrUnavailable} }

// Usage is the token count of one completion.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// maxResponseBytes bounds memory spent on an untrusted endpoint's reply.
const maxResponseBytes = 1 << 20

// Client is an OpenAI-compatible Chat Completions client.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	cfg        config.Model
}

// New builds a client from config; a nil httpClient uses a default, and deadlines come from ctx.
func New(cfg config.Model, httpClient *http.Client) (*Client, error) {
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	apiKey, err := config.Secret(cfg.APIKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("llm api key: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if apiKey != "" {
		withoutRedirects := *httpClient
		withoutRedirects.CheckRedirect = refuseRedirect
		httpClient = &withoutRedirects
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    baseURL,
		apiKey:     apiKey,
		cfg:        cfg,
	}, nil
}

// Complete sends req and returns the model's assistant message.
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	callCtx := ctx
	if c.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeoutCause(ctx, c.cfg.Timeout, fmt.Errorf("model call exceeded %s: %w", c.cfg.Timeout, context.DeadlineExceeded))
		defer cancel()
	}
	resp, err := c.complete(callCtx, req)
	// A caller that gave up must not be failed over; its own deadline ends the turn.
	if err != nil && ctx.Err() == nil && (errors.As(err, new(transportError)) || callCtx.Err() != nil) {
		err = unavailableError{err}
	}
	resp.Model = cmp.Or(c.cfg.Name, c.cfg.Model)
	return resp, err
}

func (c *Client) complete(ctx context.Context, req Request) (_ Response, err error) {
	body, err := json.Marshal(toWireRequest(c.cfg, req))
	if err != nil {
		return Response{}, fmt.Errorf("llm: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("llm: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.cfg.Streaming() {
		httpReq.Header.Set("Accept", "text/event-stream, application/json")
	}
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Response{}, transportError{fmt.Errorf("llm: request completion: %w", err)}
	}
	defer func() {
		if closeErr := httpResp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("llm: close completion response: %w", closeErr))
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		err := fmt.Errorf("llm: completion HTTP %s: %s", httpResp.Status, bytes.TrimSpace(snippet))
		if c.cfg.RetriesStatus(httpResp.StatusCode) {
			return Response{}, transportError{err}
		}
		return Response{}, err
	}

	// A connection dropped mid-reply is the endpoint failing, unlike a reply that fails to parse.
	httpResp.Body = transportReader{httpResp.Body}
	wire, err := readResponse(httpResp)
	if err != nil {
		// A deadline hit mid-stream surfaces as a read error; report the cause.
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("llm: read completion response: %w", context.Cause(ctx))
		}
		return Response{}, fmt.Errorf("llm: %w", err)
	}
	msg, err := fromWireResponse(wire)
	if err != nil {
		return Response{}, fmt.Errorf("llm: %w", err)
	}
	return Response{Message: msg, Usage: wire.usage()}, nil
}

// transportError is a failure of the endpoint rather than of the request or the reply.
type transportError struct{ error }

func (e transportError) Unwrap() error { return e.error }

type transportReader struct{ io.ReadCloser }

func (r transportReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		err = transportError{err}
	}
	return n, err
}

// readResponse parses a stream, or a plain JSON body from an endpoint that ignores stream.
func readResponse(httpResp *http.Response) (wireResponse, error) {
	if mediaType, _, _ := mime.ParseMediaType(httpResp.Header.Get("Content-Type")); mediaType == "text/event-stream" {
		return readStream(httpResp.Body)
	}
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
	if err != nil {
		return wireResponse{}, fmt.Errorf("read completion response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return wireResponse{}, fmt.Errorf("completion response exceeds %d bytes", maxResponseBytes)
	}
	var wire wireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return wireResponse{}, fmt.Errorf("decode completion response: %w", err)
	}
	return wire, nil
}

// refuseRedirect keeps the API key from reaching another origin or a downgraded scheme.
func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("refusing to follow redirect to %s for an authenticated LLM endpoint", req.URL.Redacted())
}

// normalizeBaseURL trims a trailing slash and rejects parts lost when appending a path.
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse llm base URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("llm base URL %q must use http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("llm base URL %q has no host", raw)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("llm base URL %q must not carry query, fragment, or userinfo", raw)
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}
