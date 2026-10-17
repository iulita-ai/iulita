package browseroauth

import (
	"context"
	"errors"
)

// SavedMemoryReader reads only the explicitly enabled saved-memory view.
type SavedMemoryReader interface {
	ReadMobileSavedKnowledge(context.Context, string) (string, error)
}

// ReadSavedMemory is opt-in ACCOUNT mode only. Its callers snapshot this bounded
// text at admission, never invoke legacy session search or background extraction.
func (a PasswordAccounts) ReadSavedMemory(ctx context.Context, owner string) (string, error) {
	source, ok := a.Users.(SavedMemoryReader)
	if !ok {
		return "", errors.New("saved account memory unavailable")
	}
	return source.ReadMobileSavedKnowledge(ctx, owner)
}

// ReadRelevantMemory snapshots only saved owner knowledge relevant to this turn.
func (a PasswordAccounts) ReadRelevantMemory(ctx context.Context, owner, query string) (string, error) {
	if source, ok := a.Users.(interface {
		ReadMobileRelevantKnowledge(context.Context, string, string) (string, error)
	}); ok {
		return source.ReadMobileRelevantKnowledge(ctx, owner, query)
	}
	return a.ReadSavedMemory(ctx, owner)
}
