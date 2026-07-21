package openaichatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Chat Completions wire types. Only the fields the planner produces or
// consumes are modeled; unknown response fields are ignored by the JSON
// decoder.

// chatRequest is the POST body sent to /chat/completions.
type chatRequest struct {
	Model    string       `json:"model"`
	Messages []reqMessage `json:"messages"`
	Tools    []toolDef    `json:"tools,omitempty"`
}

// reqMessage is one message supplied to the model (system or user).
type reqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// toolDef is one function offered to the model.
type toolDef struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// chatResponse is the subset of the completion response the planner reads.
type chatResponse struct {
	Choices []choice `json:"choices"`
}

type choice struct {
	Message respMessage `json:"message"`
}

// respMessage is the assistant message the model returned: free-form
// content, tool calls, or both.
type respMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []toolCall `json:"tool_calls"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

// functionCall carries the called function's name and its arguments as
// a JSON-encoded string, per the Chat Completions API.
type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// maxResponseBytes caps a completion response body; a plan response is
// small, so 1 MiB bounds memory without rejecting real responses.
const maxResponseBytes = 1 << 20

// chatComplete POSTs req to baseURL's /chat/completions and decodes the
// response. A non-empty apiKey is sent as a bearer token (empty omits
// it, for keyless servers). Non-2xx status and oversized bodies error.
func chatComplete(ctx context.Context, client *http.Client, baseURL, apiKey string, req chatRequest) (_ chatResponse, err error) {
	body, err := json.Marshal(req)
	if err != nil {
		return chatResponse{}, fmt.Errorf("openai: encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, fmt.Errorf("openai: create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return chatResponse{}, fmt.Errorf("openai: request completion: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("openai: close completion response: %w", closeErr))
		}
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return chatResponse{}, fmt.Errorf("openai: completion HTTP %s: %s", response.Status, bytes.TrimSpace(snippet))
	}

	// Bound the body so an untrusted endpoint cannot exhaust memory.
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return chatResponse{}, fmt.Errorf("openai: read completion response: %w", err)
	}
	if int64(len(data)) > maxResponseBytes {
		return chatResponse{}, fmt.Errorf("openai: completion response exceeds %d bytes", maxResponseBytes)
	}
	var decoded chatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return chatResponse{}, fmt.Errorf("openai: decode completion response: %w", err)
	}
	return decoded, nil
}
