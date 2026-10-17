package storage

import "context"

// AccountFacts deletes saved owner knowledge independently of its source chat.
// Implementations must retain the legacy mobile transcript exclusion.
type AccountFacts interface {
	DeleteAccountFact(context.Context, string, int64) (bool, error)
	DeleteAccountFactsByQuery(context.Context, string, string) (int64, error)
}
