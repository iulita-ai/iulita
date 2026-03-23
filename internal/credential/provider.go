package credential

import "context"

// NilProvider is a CredentialProvider that always returns ErrNotFound.
type NilProvider struct{}

// Resolve always returns ErrNotFound.
func (NilProvider) Resolve(_ context.Context, _ string) (string, error) {
	return "", ErrNotFound
}

// ResolveForUser always returns ErrNotFound.
func (NilProvider) ResolveForUser(_ context.Context, _, _ string) (string, error) {
	return "", ErrNotFound
}

// IsAvailable always returns false.
func (NilProvider) IsAvailable(_ context.Context, _ string) bool { return false }
