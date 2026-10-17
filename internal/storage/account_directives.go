package storage

import "context"

// AccountDirectives is the explicit owner-scoped preference API. Channel source
// IDs are not invented for account-wide instructions. Empty content is a clear
// tombstone so old channel directives cannot silently become active again.
type AccountDirectives interface {
	ReadAccountDirective(context.Context, string) (string, error)
	WriteAccountDirective(context.Context, string, string) error
}
