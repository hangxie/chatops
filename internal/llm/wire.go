package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Chat Completions wire types, limited to the fields chatops uses.

type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	// ReasoningEffort "none" disables thinking on OpenAI, Gemini, and Ollama.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ChatTemplateKwargs carries enable_thinking for vLLM, SGLang, and llama.cpp.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type wireMessage struct {
	Role string `json:"role"`
	// Content is null on an assistant message that only calls tools.
	Content    *string        `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
}

// wireFunctionCall carries arguments as a JSON-encoded string, per the API.
type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireResponse struct {
	Choices []struct {
		Message wireMessage `json:"message"`
		// FinishReason is optional because some local servers omit it.
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// thinkBlock matches a leading reasoning block some servers leave even with thinking disabled.
var thinkBlock = regexp.MustCompile(`(?s)^\s*<think>.*?</think>`)

func toWireRequest(model string, disableThinking bool, req Request) wireRequest {
	wire := wireRequest{Model: model, Messages: make([]wireMessage, 0, len(req.Messages))}
	for _, msg := range req.Messages {
		wire.Messages = append(wire.Messages, toWireMessage(msg))
	}
	for _, tool := range req.Tools {
		wire.Tools = append(wire.Tools, wireTool{
			Type:     "function",
			Function: wireFunction(tool),
		})
	}
	// Each endpoint honors a different switch, so send both; opt-in since strict endpoints reject them.
	if disableThinking {
		wire.ReasoningEffort = "none"
		wire.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
	return wire
}

func toWireMessage(msg Message) wireMessage {
	wire := wireMessage{Role: string(msg.Role), ToolCallID: msg.ToolCallID}
	if msg.Content != "" || len(msg.ToolCalls) == 0 {
		content := msg.Content
		wire.Content = &content
	}
	for _, call := range msg.ToolCalls {
		wire.ToolCalls = append(wire.ToolCalls, wireToolCall{
			ID:       call.ID,
			Type:     "function",
			Function: wireFunctionCall{Name: call.Name, Arguments: call.Arguments},
		})
	}
	return wire
}

func fromWireResponse(resp wireResponse) (Message, error) {
	if len(resp.Choices) == 0 {
		return Message{}, errors.New("response has no choices")
	}
	switch reason := resp.Choices[0].FinishReason; reason {
	case "length":
		return Message{}, fmt.Errorf("response truncated at the token limit (finish_reason %s)", reason)
	case "content_filter":
		return Message{}, fmt.Errorf("response withheld by a content filter (finish_reason %s)", reason)
	}
	wire := resp.Choices[0].Message
	msg := Message{Role: RoleAssistant}
	if wire.Content != nil {
		msg.Content = strings.TrimSpace(thinkBlock.ReplaceAllString(*wire.Content, ""))
	}
	for i, call := range wire.ToolCalls {
		// Some local servers omit IDs, but tool results must reference one.
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		args := call.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: id, Name: call.Function.Name, Arguments: args})
	}
	if msg.Content == "" && len(msg.ToolCalls) == 0 {
		return Message{}, errors.New("response has neither content nor tool calls")
	}
	return msg, nil
}
