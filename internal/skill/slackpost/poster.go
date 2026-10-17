package slackpost

import "context"

// ChannelPoster is the seam to the bot channel(s) used to post. Implemented by
// channelmgr.Manager (routing across running Slack instances). Defined here
// (accept-interfaces) so this package does not import channelmgr.
type ChannelPoster interface {
	// WriteMode returns "off" | "draft" | "auto" for a channel; "off" means the
	// bot may not post there.
	WriteMode(ctx context.Context, channelID string) string
	// PostToChannel posts to an allow-listed channel and returns the message ts.
	// It re-checks permission and guardrails internally (single chokepoint).
	PostToChannel(ctx context.Context, channelID, text string) (ts string, err error)
}

// BoundChannelPoster pins native approval to one concrete bot route. A changed
// connection or deterministic route selection cannot reuse the old approval.
type BoundChannelPoster interface {
	NativePostBinding(ctx context.Context, channelID string) string
	PostBoundToChannel(ctx context.Context, channelID, text, binding string) (string, error)
}
