package mcphost

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxRenderedBytes bounds what one tool result may post into chat. Tool
// output crosses a trust boundary — an external server's result is not
// content chatops composed — so it is truncated rather than relayed whole.
const maxRenderedBytes = 16 << 10

// truncationNotice marks output cut short by maxRenderedBytes.
const truncationNotice = "\n… (truncated)"

// Render turns a tool result into the text to post into chat.
//
// Text content is the tool's own rendering and is relayed as-is. Content that
// chat cannot show inline — images, audio, linked or embedded resources — is
// named rather than dropped, so the requester can tell something came back.
// Empty output means the tool has already delivered its outcome itself (the
// reply tool posts into chat, so its result carries nothing), and callers stay
// silent rather than posting an empty message.
func Render(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if rendered := renderContent(content); rendered != "" {
			parts = append(parts, rendered)
		}
	}
	return truncate(strings.Join(parts, "\n"))
}

// renderContent renders one content block.
func renderContent(content mcp.Content) string {
	switch c := content.(type) {
	case *mcp.TextContent:
		return c.Text
	case *mcp.ImageContent:
		return fmt.Sprintf("[image: %s]", orUnknown(c.MIMEType))
	case *mcp.AudioContent:
		return fmt.Sprintf("[audio: %s]", orUnknown(c.MIMEType))
	case *mcp.ResourceLink:
		return fmt.Sprintf("[resource: %s]", firstNonEmpty(c.Title, c.Name, c.URI))
	case *mcp.EmbeddedResource:
		return renderEmbedded(c)
	default:
		return ""
	}
}

// renderEmbedded renders an embedded resource, inlining its text when it has
// any and naming it otherwise.
func renderEmbedded(c *mcp.EmbeddedResource) string {
	if c.Resource == nil {
		return "[resource]"
	}
	if c.Resource.Text != "" {
		return c.Resource.Text
	}
	return fmt.Sprintf("[resource: %s]", firstNonEmpty(c.Resource.URI, c.Resource.MIMEType))
}

// truncate bounds rendered output at maxRenderedBytes, cutting on a rune
// boundary so the result stays valid UTF-8.
func truncate(text string) string {
	if len(text) <= maxRenderedBytes {
		return text
	}
	cut := maxRenderedBytes - len(truncationNotice)
	for cut > 0 && !isRuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + truncationNotice
}

// isRuneStart reports whether b starts a UTF-8 rune (i.e. is not a
// continuation byte).
func isRuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

func orUnknown(value string) string {
	return firstNonEmpty(value, "unknown type")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
