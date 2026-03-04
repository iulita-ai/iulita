package channel

import "context"

// ChannelCaps is a bitmask of capabilities a channel supports.
type ChannelCaps uint32

const (
	CapStreaming ChannelCaps = 1 << iota // supports incremental stream edits
	CapMarkdown                          // renders markdown (bold, code blocks, links)
	CapReactions                         // can attach emoji reactions to messages
	CapButtons                           // can render inline buttons (confirmation prompts)
	CapTyping                            // shows typing indicator
	CapHTML                              // renders HTML (webchat)
)

// CapabilityProvider is an optional interface channels implement to declare their features.
type CapabilityProvider interface {
	Capabilities() ChannelCaps
}

// Has returns true if all of the queried bits are set.
func (c ChannelCaps) Has(q ChannelCaps) bool { return c&q == q }

type capsKey struct{}

// WithCaps returns a context enriched with channel capabilities.
func WithCaps(ctx context.Context, caps ChannelCaps) context.Context {
	return context.WithValue(ctx, capsKey{}, caps)
}

// CapsFrom extracts channel capabilities from context.
func CapsFrom(ctx context.Context) ChannelCaps {
	v, _ := ctx.Value(capsKey{}).(ChannelCaps)
	return v
}
