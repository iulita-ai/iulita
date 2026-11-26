package telegram

import (
	"context"
	"sync"
	"time"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

type businessCandidatesKey struct{}
type businessCandidate struct {
	updateID         int64
	expectedRevision uint64
	scope            business.Scope
	messageID        int
	revision         int64
	generation       string
}
type businessCandidates struct {
	items map[business.Scope]businessCandidate
}

func (b *businessCandidates) add(candidate businessCandidate) {
	if b.items == nil {
		b.items = make(map[business.Scope]businessCandidate)
	}
	if _, exists := b.items[candidate.scope]; exists || len(b.items) < 100 {
		b.items[candidate.scope] = candidate
	}
}

type businessQueuedWork struct {
	candidate businessCandidate
	again     bool
}
type businessWorkQueue struct {
	mu         sync.Mutex
	generation string
	active     map[business.Scope]*businessQueuedWork
}

func (c *Channel) businessDeliveryGeneration() string {
	if c.businessWork == nil {
		return c.businessGeneration
	}
	c.businessWork.mu.Lock()
	defer c.businessWork.mu.Unlock()
	return c.businessWork.generation
}
func (c *Channel) rotateBusinessGeneration(generation string) {
	if c.businessWork == nil {
		c.businessGeneration = generation
		return
	}
	c.businessWork.mu.Lock()
	defer c.businessWork.mu.Unlock()
	c.businessGeneration = generation
	c.businessWork.generation = generation
}
func (c *Channel) candidateCurrent(ctx context.Context, candidate businessCandidate) error {
	if c.business == nil || ctx.Err() != nil || candidate.generation != c.businessDeliveryGeneration() {
		return business.ErrStale
	}
	metadata, e := c.business.GetState(ctx, candidate.scope.OwnerID, candidate.scope.Key)
	if e != nil {
		return e
	}
	if metadata.Connection.DeliveryGeneration != candidate.generation || metadata.Connection.NeedsRefresh || metadata.Connection.Paused || metadata.Connection.Quarantined || !metadata.Connection.Enabled {
		return business.ErrDenied
	}
	peer := metadata.Peers[candidate.scope.PeerID]
	if peer == nil || peer.Revision != candidate.expectedRevision {
		return business.ErrStale
	}
	messages, e := c.business.Messages(ctx, candidate.scope)
	if e != nil {
		return e
	}
	if len(messages) == 0 {
		return business.ErrStale
	}
	latest := messages[len(messages)-1]
	if latest.ID != candidate.messageID || latest.Revision != candidate.revision || latest.Direction != "incoming" || latest.SenderIsBot {
		return business.ErrStale
	}
	return nil
}
func (c *Channel) scheduleBusinessCandidate(ctx context.Context, candidate businessCandidate) {
	if c.businessWork == nil || ctx.Err() != nil {
		return
	}
	q := c.businessWork
	q.mu.Lock()
	if job := q.active[candidate.scope]; job != nil {
		if job.candidate.updateID != candidate.updateID {
			job.candidate = candidate
			job.again = true
		}
		q.mu.Unlock()
		return
	}
	if len(q.active) >= 100 {
		q.mu.Unlock()
		c.business.ObserveTransport(business.Observation{Operation: "queue", Kind: "candidate", Outcome: "pending"})
		if c.logger != nil {
			c.logger.Warn("Telegram account automatic work queue is full")
		}
		return
	}
	q.active[candidate.scope] = &businessQueuedWork{candidate: candidate}
	c.wg.Add(1)
	q.mu.Unlock()
	go func() {
		defer c.wg.Done()
		for {
			q.mu.Lock()
			job := q.active[candidate.scope]
			current := job.candidate
			job.again = false
			q.mu.Unlock()
			currentErr := c.candidateCurrent(ctx, current)
			if currentErr == nil {
				if processErr := c.business.Process(ctx, current.scope); processErr != nil && c.logger != nil {
					c.logger.Warn("Telegram account processing did not complete")
				}
			}
			if currentErr == nil || terminalBusinessCandidate(currentErr) {
				c.completeBusinessCandidate(ctx, current)
			}
			q.mu.Lock()
			if ctx.Err() != nil || !job.again {
				delete(q.active, candidate.scope)
				q.mu.Unlock()
				return
			}
			q.mu.Unlock()
		}
	}()
}

// Serialize only this bounded local ACK with generation rotation, preventing a
// late old-generation worker from deleting a reused update ID after idle reset.
func (c *Channel) completeBusinessCandidate(ctx context.Context, candidate businessCandidate) {
	if ctx.Err() != nil || c.businessPoll == nil {
		return
	}
	cleanup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	q := c.businessWork
	q.mu.Lock()
	defer q.mu.Unlock()
	if cleanup.Err() != nil || q.generation != candidate.generation {
		return
	}
	if err := c.businessPoll.AckTelegramBusinessPoll(cleanup, c.instanceID, c.botID, candidate.updateID); err != nil && c.logger != nil {
		c.logger.Warn("Telegram account work receipt remains pending")
	}
}
