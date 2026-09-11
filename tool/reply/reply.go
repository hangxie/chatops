// Package reply implements the host tool that posts text back into a chat
// conversation, so planners can express "say this to the requester" as an
// ordinary tool call alongside operational tool calls.
//
// Unlike the built-in operational tools, reply is not served by an MCP server:
// it is bound to a live chat.Conn — the connection the message being answered
// arrived on — which is host state rather than anything a server could hold.
// It is therefore registered with the host directly, exactly as an MCP host
// adds its own local tools to the catalog it offers the model, and it is the
// one tool that never leaves the process.
//
// The tool reads "text" for the message to post and optional "choices" for
// interactive responses. The target conversation is not an argument: the host
// carries it on the context (see chat.WithConversation), so the model never
// selects it and replies cannot be misrouted. Posting is the whole outcome, so
// the result carries no content and callers relaying tool output do not
// double-post.
package reply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/chat"
)

// ToolName is the model-facing name of the reply tool.
const ToolName = "reply"

// Args is the tool's input schema.
type Args struct {
	Text    string   `json:"text" jsonschema:"The message text to post back to the requester."`
	Choices []Choice `json:"choices,omitempty" jsonschema:"Optional bounded set of responses to offer the requester as interactive controls."`
}

// Choice is one response offered to the human receiving the reply. Chat
// backends may render choices as interactive controls and otherwise retain
// the message text as a plain-text fallback.
type Choice struct {
	Label string `json:"label" jsonschema:"The text shown on the control."`
	Value string `json:"value" jsonschema:"The message sent back when the control is chosen."`
}

// argsSchema is the input schema inferred from Args, so the definition the
// model sees and the struct the handler decodes into cannot drift apart. The
// inference runs on a type this package owns, so failure is a programmer
// error.
var argsSchema = mustSchema(jsonschema.For[Args](nil))

// resolvedArgs validates a call against argsSchema.
//
// A tool served by an MCP server has its arguments checked against the
// declared schema before the handler runs. This one is called directly by the
// host, so nothing checks them unless it does: decoding alone accepts fields
// the schema forbids and misses required ones, so a choice with a label and
// no value would reach the chat backend despite being advertised as invalid.
var resolvedArgs = mustResolve(argsSchema.Resolve(nil))

// mustSchema returns the inferred schema, panicking on failure. Inference
// runs on a type this package owns, so a failure is a programmer error.
func mustSchema(schema *jsonschema.Schema, err error) *jsonschema.Schema {
	if err != nil {
		panic(fmt.Sprintf("reply: infer input schema: %v", err))
	}
	return schema
}

// mustResolve returns the resolved schema, panicking on failure. It resolves
// a schema this package inferred, so a failure is a programmer error.
func mustResolve(resolved *jsonschema.Resolved, err error) *jsonschema.Resolved {
	if err != nil {
		panic(fmt.Sprintf("reply: resolve input schema: %v", err))
	}
	return resolved
}

// Definition is the model-facing description of the reply tool, for
// registering it with the host catalog.
func Definition() *mcp.Tool {
	return &mcp.Tool{
		Name: ToolName,
		Description: "Post a message back to the person who sent the request, " +
			"for answers, clarifying questions, or acknowledgements.",
		InputSchema: argsSchema,
	}
}

// Tool posts text into conversations of one chat connection.
type Tool struct {
	conn chat.Conn
}

// Open returns a reply tool bound to conn. The connection stays owned by the
// caller: the tool sends on it but never closes it.
//
// A nil connection is a mistake at the wiring site rather than a runtime
// condition, so it panics, as the registries in this repository do.
func Open(_ context.Context, conn chat.Conn) *Tool {
	if conn == nil {
		panic("reply: open with nil connection")
	}
	return &Tool{conn: conn}
}

// Invoke posts args.Text into the conversation carried by ctx. A context
// without a conversation, or empty text, is an error; errors from the
// underlying send (e.g. wrapping chat.ErrUnknownConversation) are passed
// through wrapped.
func (t *Tool) Invoke(ctx context.Context, args Args) (*mcp.CallToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reply: %w", err)
	}
	conversation, ok := chat.ConversationFrom(ctx)
	if !ok || conversation == "" {
		return nil, errors.New("reply: no target conversation")
	}
	if args.Text == "" {
		return nil, errors.New(`reply: no "text" argument`)
	}
	var choices []chat.Choice
	if args.Choices != nil {
		choices = make([]chat.Choice, len(args.Choices))
		for i, choice := range args.Choices {
			choices[i] = chat.Choice{Label: choice.Label, Value: choice.Value}
		}
	}
	msg := chat.Message{ConversationID: conversation, Text: args.Text, Choices: choices}
	if err := t.conn.Send(ctx, msg); err != nil {
		return nil, fmt.Errorf("reply: %w", err)
	}
	// Posting is the whole outcome: no content, so a caller relaying tool
	// output stays silent rather than double-posting.
	return &mcp.CallToolResult{}, nil
}

// Handler adapts Invoke to the host-tool calling convention: it decodes the
// call's arguments into Args before invoking. Decoding goes through JSON so
// the arguments are read exactly as they would be by a server across a
// transport, keeping the host tool and an MCP tool behaviourally identical.
func (t *Tool) Handler(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	decoded, err := decodeArgs(args)
	if err != nil {
		return nil, err
	}
	return t.Invoke(ctx, decoded)
}

// decodeArgs validates a raw argument bag against the declared schema and
// converts it into Args.
func decodeArgs(args map[string]any) (Args, error) {
	if len(args) == 0 {
		return Args{}, nil
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return Args{}, fmt.Errorf("reply: encode arguments: %w", err)
	}
	// Validation reads the arguments as given, so it sees every field the
	// caller sent rather than only the ones Args happens to name.
	if err := resolvedArgs.Validate(args); err != nil {
		return Args{}, fmt.Errorf("reply: invalid arguments: %w", err)
	}
	// The schema was inferred from Args and the arguments have just been
	// checked against it, so they decode into it.
	var decoded Args
	_ = json.Unmarshal(encoded, &decoded)
	return decoded, nil
}

// Close releases nothing; the chat connection is owned by the caller and
// stays open.
func (t *Tool) Close() error {
	return nil
}
