package telegrambusiness

import "context"

// SourceImage is a bounded transient image supplied by the trusted processor.
type SourceImage struct {
	Data      []byte
	MediaType string
}

// ProcessedSource contains untrusted derived text and transient images.
type ProcessedSource struct {
	Text        string
	Images      []SourceImage
	Omitted     int
	Extractions []SourceExtraction
}

// SourceExtraction binds derived text to its original message revision.
type SourceExtraction struct {
	MessageID int
	Revision  int64
	Text      string
}

// SourceProcessor processes only bounded attachments in an admitted scope.
type SourceProcessor interface {
	Descriptor(Scope) string
	Process(context.Context, Scope, []Message) (ProcessedSource, error)
}
type sourceGuardKey struct{}

// WithSourceGuard binds trusted admission to a request-local source operation.
func WithSourceGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, sourceGuardKey{}, guard)
}

// CheckSourceGuard rejects missing, cancelled or stale source admission.
func CheckSourceGuard(ctx context.Context) error {
	guard, ok := ctx.Value(sourceGuardKey{}).(func(context.Context) error)
	if !ok || guard == nil {
		return ErrDenied
	}
	if checkErr := ctx.Err(); checkErr != nil {
		return checkErr
	}
	return guard(ctx)
}
