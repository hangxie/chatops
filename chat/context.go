package chat

import "context"

// conversationKey is the unexported context key carrying the conversation a
// request is being handled for.
type conversationKey struct{}

// WithConversation returns a context carrying the conversation ID a request
// is being handled for.
//
// Tool calls are dispatched by name and arguments alone, so a tool that acts
// on the requester's conversation — posting a reply, asking a question — has
// no argument naming it: the conversation is the host's state, not something
// the model chooses. Carrying it on the context keeps it out of the
// model-facing schema while still reaching the handler.
func WithConversation(ctx context.Context, conversationID string) context.Context {
	return context.WithValue(ctx, conversationKey{}, conversationID)
}

// ConversationFrom returns the conversation ID carried by ctx. The second
// result is false when ctx carries none, which is the case for callers that
// are not handling an inbound message.
func ConversationFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(conversationKey{}).(string)
	return id, ok
}
