package llm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// wireChunk is one server-sent event of a streamed completion.
type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// streamState folds chunks into the shape of a non-streamed response.
type streamState struct {
	content      strings.Builder
	calls        []wireToolCall
	byIndex      map[int]int // stream index to position in calls
	finishReason string
	usage        *wireUsage
	size         int
}

// readStream assembles a response from server-sent events, one whole JSON chunk per data line.
func readStream(body io.Reader) (wireResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxResponseBytes)
	var state streamState
	done := false
	for !done && scanner.Scan() {
		data, ok := strings.CutPrefix(strings.TrimRight(scanner.Text(), "\r"), "data:")
		if !ok {
			continue // blank separators, comments, and event names
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			done = true
			continue
		}
		var chunk wireChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return wireResponse{}, fmt.Errorf("decode stream chunk: %w", err)
		}
		if err := state.add(chunk); err != nil {
			return wireResponse{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return wireResponse{}, fmt.Errorf("read completion stream: %w", err)
	}
	// Without [DONE] or a finish reason, the connection may have dropped mid-answer.
	if !done && state.finishReason == "" {
		return wireResponse{}, errors.New("stream ended before the response finished")
	}
	// A nameless call means the provider split one call across indices, which cannot be reassembled safely.
	for index, pos := range state.byIndex {
		if state.calls[pos].Function.Name == "" {
			return wireResponse{}, fmt.Errorf("stream tool call index %d has no name", index)
		}
	}
	return state.response(), nil
}

func (s *streamState) add(chunk wireChunk) error {
	if chunk.Error != nil {
		return fmt.Errorf("stream error: %s", chunk.Error.Message)
	}
	if chunk.Usage != nil {
		s.usage = chunk.Usage
	}
	if len(chunk.Choices) == 0 {
		return nil
	}
	choice := chunk.Choices[0]
	if choice.FinishReason != "" {
		s.finishReason = choice.FinishReason
	}
	s.content.WriteString(choice.Delta.Content)
	s.size += len(choice.Delta.Content)
	for _, delta := range choice.Delta.ToolCalls {
		if delta.Index < 0 {
			return fmt.Errorf("stream tool call index %d out of range", delta.Index)
		}
		call := s.call(delta.Index)
		// Some servers repeat the ID and name on every chunk, so keep the first.
		if call.ID == "" {
			call.ID = delta.ID
		}
		if call.Function.Name == "" {
			call.Function.Name = delta.Function.Name
		}
		call.Function.Arguments += delta.Function.Arguments
		s.size += len(delta.Function.Arguments)
	}
	if s.size > maxResponseBytes {
		return fmt.Errorf("completion response exceeds %d bytes", maxResponseBytes)
	}
	return nil
}

// call finds the call an index identifies; indices may be sparse, and calls keep the order they first appeared in.
func (s *streamState) call(index int) *wireToolCall {
	if s.byIndex == nil {
		s.byIndex = map[int]int{}
	}
	pos, ok := s.byIndex[index]
	if !ok {
		pos = len(s.calls)
		s.calls = append(s.calls, wireToolCall{Type: "function"})
		s.byIndex[index] = pos
	}
	return &s.calls[pos]
}

func (s *streamState) response() wireResponse {
	content := s.content.String()
	message := wireMessage{Role: string(RoleAssistant), Content: &content, ToolCalls: s.calls}
	return wireResponse{Choices: []wireChoice{{Message: message, FinishReason: s.finishReason}}, Usage: s.usage}
}
