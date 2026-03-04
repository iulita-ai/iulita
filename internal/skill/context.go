package skill

import "context"

type ctxKey int

const (
	ctxChatID ctxKey = iota
	ctxUserID
	ctxUserRole
	ctxDocuments
)

// DocumentAttachment holds a file received from a channel, available to skills via context.
type DocumentAttachment struct {
	Data     []byte
	MimeType string
	Filename string
}

// WithChatID returns a context with the chat ID set.
func WithChatID(ctx context.Context, chatID string) context.Context {
	return context.WithValue(ctx, ctxChatID, chatID)
}

// ChatIDFrom extracts the chat ID from the context, or returns empty string.
func ChatIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxChatID).(string)
	return v
}

// WithUserID returns a context with the user ID set.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxUserID, userID)
}

// UserIDFrom extracts the user ID from the context, or returns empty string.
func UserIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserID).(string)
	return v
}

// WithUserRole returns a context with the user role set.
func WithUserRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, ctxUserRole, role)
}

// UserRoleFrom extracts the user role from the context, or returns empty string.
func UserRoleFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserRole).(string)
	return v
}

// WithDocuments returns a context with document attachments set.
func WithDocuments(ctx context.Context, docs []DocumentAttachment) context.Context {
	return context.WithValue(ctx, ctxDocuments, docs)
}

// DocumentsFrom extracts document attachments from the context, or returns nil.
func DocumentsFrom(ctx context.Context) []DocumentAttachment {
	v, _ := ctx.Value(ctxDocuments).([]DocumentAttachment)
	return v
}
