package dayview

import (
	"context"
	"time"
)

// Card contains server-only authority. It must never be serialized into public_data.
type Card struct {
	DesiredState      string
	DeliveredRevision int64
	ID                string `bun:",pk"`
	Owner             string
	Instance          string
	Chat              int64
	Thread            int
	Message           int
	Revision          int64
	ItemsJSON         string
	Expires           time.Time
}

// Operation is a durable receipt for an immutable approved command.
type Operation struct {
	ResourceKey      string
	PayloadCompacted bool
	Conversation     string
	Instance         string
	CommandJSON      string
	ApprovedAt       time.Time
	LastCheckedAt    time.Time
	ID               string `bun:",pk"`
	CardID           string
	Owner            string
	Status           string
	CreatedAt        time.Time
}

// Store persists private projections and durable command receipts.
type Store interface {
	SaveDayCard(context.Context, *Card) error
	GetDayCard(context.Context, string) (*Card, error)
	ClaimDayOperation(context.Context, *Operation) (bool, error)
	FinishDayOperation(context.Context, string, string) error
}
