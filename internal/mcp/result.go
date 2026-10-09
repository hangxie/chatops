package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Result is a tool result rendered as text for the model.
type Result struct {
	Text    string
	IsError bool
}

// newResult renders content as text, with placeholders for binary and structured content as fallback.
func newResult(r *mcpsdk.CallToolResult) Result {
	parts := make([]string, 0, len(r.Content))
	for _, content := range r.Content {
		parts = append(parts, renderContent(content))
	}
	if len(parts) == 0 && r.StructuredContent != nil {
		encoded, err := json.Marshal(r.StructuredContent)
		if err != nil {
			parts = append(parts, "[structured content could not be encoded]")
		} else {
			parts = append(parts, string(encoded))
		}
	}
	return Result{Text: strings.Join(parts, "\n"), IsError: r.IsError}
}

func renderContent(content mcpsdk.Content) string {
	switch c := content.(type) {
	case *mcpsdk.TextContent:
		return c.Text
	case *mcpsdk.ImageContent:
		return fmt.Sprintf("[%s image omitted]", c.MIMEType)
	case *mcpsdk.AudioContent:
		return fmt.Sprintf("[%s audio omitted]", c.MIMEType)
	case *mcpsdk.ResourceLink:
		return fmt.Sprintf("[resource link: %s]", c.URI)
	case *mcpsdk.EmbeddedResource:
		if c.Resource == nil {
			return "[resource omitted]"
		}
		if c.Resource.Text != "" {
			return c.Resource.Text
		}
		return fmt.Sprintf("[resource %s omitted]", c.Resource.URI)
	}
	return fmt.Sprintf("[%T content omitted]", content)
}
