// Package llm is a small client for OpenAI-compatible Chat Completions endpoints.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

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

// Request is one completion request.
type Request struct {
	Messages []Message
	Tools    []ToolSpec
}

// Response holds the assistant message: final text, tool calls, or both.
type Response struct {
	Message Message
}

// maxResponseBytes bounds memory spent on an untrusted endpoint's reply.
const maxResponseBytes = 1 << 20

// Client is an OpenAI-compatible Chat Completions client.
type Client struct {
	httpClient      *http.Client
	baseURL         string
	apiKey          string
	model           string
	disableThinking bool
	sampling        config.Sampling
}

// New builds a client from config; a nil httpClient uses a default, and deadlines come from ctx.
func New(cfg config.LLM, httpClient *http.Client) (*Client, error) {
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
		httpClient:      httpClient,
		baseURL:         baseURL,
		apiKey:          apiKey,
		model:           cfg.Model,
		disableThinking: cfg.DisableThinking,
		sampling:        cfg.Sampling,
	}, nil
}

// Complete sends req and returns the model's assistant message.
func (c *Client) Complete(ctx context.Context, req Request) (_ Response, err error) {
	body, err := json.Marshal(toWireRequest(c.model, c.disableThinking, c.sampling, req))
	if err != nil {
		return Response{}, fmt.Errorf("llm: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("llm: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("llm: request completion: %w", err)
	}
	defer func() {
		if closeErr := httpResp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("llm: close completion response: %w", closeErr))
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		return Response{}, fmt.Errorf("llm: completion HTTP %s: %s", httpResp.Status, bytes.TrimSpace(snippet))
	}

	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
	if err != nil {
		return Response{}, fmt.Errorf("llm: read completion response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return Response{}, fmt.Errorf("llm: completion response exceeds %d bytes", maxResponseBytes)
	}
	var wire wireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return Response{}, fmt.Errorf("llm: decode completion response: %w", err)
	}
	msg, err := fromWireResponse(wire)
	if err != nil {
		return Response{}, fmt.Errorf("llm: %w", err)
	}
	return Response{Message: msg}, nil
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
