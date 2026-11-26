package telegrambusiness

import (
	"context"
	"encoding/json"
	"time"
)

// PollState is the application-owned receipt cursor, never the SDK's in-memory
// lastUpdateID. A cursor can advance only after business state is committed and
// ordinary updates have been durably admitted to the encrypted delivery queue.
type PollState struct {
	NextOffset int64
	UpdatedAt  time.Time
}

// PollEntry contains an ordinary bot update or versioned metadata-only business
// processing receipt already admitted by the trusted
// transport. Business envelopes MUST NOT be put here: their pre-consent policy
// permits only the metadata retained by the business service. Process receipts
// contain exact scope/revision/generation IDs, never business message bodies.
type PollEntry struct {
	UpdateID int64
	Payload  json.RawMessage
}

// PollRepository separates cursor/lease durability from conversation authority.
// Implementations must encrypt Payload and refuse plaintext fallback. Stage is
// atomic with cursor advancement. Lease guards multiple local app consumers;
// Telegram conflicts still fail closed for consumers outside this database.
type PollRepository interface {
	AcquireTelegramBusinessPoll(context.Context, string, int64, string, time.Time, time.Duration) (bool, error)
	ReleaseTelegramBusinessPoll(context.Context, string, int64, string) error
	LoadTelegramBusinessPoll(context.Context, string, int64) (PollState, error)
	StageTelegramBusinessPoll(context.Context, string, int64, string, int64, []PollEntry, time.Time) error
	PendingTelegramBusinessPoll(context.Context, string, int64, int) ([]PollEntry, error)
	AckTelegramBusinessPoll(context.Context, string, int64, int64) error
}
