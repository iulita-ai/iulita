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
