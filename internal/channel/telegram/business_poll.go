package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

// SetBusinessPollRepository installs durable intake before polling starts.
func (c *Channel) SetBusinessPollRepository(repo business.PollRepository) { c.businessPoll = repo }

func (c *Channel) pollBusinessUpdates(ctx context.Context, dispatch func(models.Update)) error {
	repo := c.businessPoll
	if c.businessWork == nil {
		c.businessWork = &businessWorkQueue{generation: c.businessGeneration, active: make(map[business.Scope]*businessQueuedWork)}
	}
	lease := uuid.NewString()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if releaseErr := repo.ReleaseTelegramBusinessPoll(cleanup, c.instanceID, c.botID, lease); releaseErr != nil && c.logger != nil {
			c.logger.Warn("Telegram account poll lease release failed")
		}
	}()
	var webhook struct {
		URL string `json:"url"`
	}
	if checkErr := c.businessCall(ctx, "getWebhookInfo", nil, &webhook); checkErr != nil {
		return checkErr
	}
	if webhook.URL != "" {
		return business.ErrDenied
	}
	for ctx.Err() == nil {
		owned, err := repo.AcquireTelegramBusinessPoll(ctx, c.instanceID, c.botID, lease, time.Now().UTC(), 2*time.Minute)
		if err != nil {
			return err
		}
		if !owned {
			if c.business != nil {
				c.business.ObserveTransport(business.Observation{Operation: "consumer", Kind: "poll", Outcome: "conflict"})
			}
			return business.ErrDenied
		}
		pending, err := repo.PendingTelegramBusinessPoll(ctx, c.instanceID, c.botID, 100)
		if err != nil {
			return err
		}
		ordinaryPending := 0
		for entryIndex := range pending {
			entry := pending[entryIndex]
			if handled, candidateErr := c.drainBusinessCandidate(ctx, entry); handled {
				if candidateErr != nil {
					return candidateErr
				}
				continue
			}
			ordinaryPending++
			update, decodeErr := decodeBusinessCompatibleUpdate(entry.Payload)
			if decodeErr == nil {
				dispatch(update)
			}
			// The legacy dispatcher owns its debounce/processing lifecycle. Here
			// the receipt means admission to that existing route, not delivery of
			// its eventual response. Business events never enter this queue.
			if checkErr := repo.AckTelegramBusinessPoll(ctx, c.instanceID, c.botID, entry.UpdateID); checkErr != nil {
				return checkErr
			}
		}
		if ordinaryPending == 100 {
			continue
		}
		cursor, err := repo.LoadTelegramBusinessPoll(ctx, c.instanceID, c.botID)
		if err != nil {
			return err
		}
		offset := cursor.NextOffset
		if !cursor.UpdatedAt.IsZero() && time.Since(cursor.UpdatedAt) > 7*24*time.Hour {
			offset = 0
			c.rotateBusinessGeneration(uuid.NewString())
		}
		var batch []json.RawMessage
		err = c.businessCall(ctx, "getUpdates", map[string]any{"offset": offset, "limit": 100, "timeout": 25, "allowed_updates": telegramAllowedUpdates()}, &batch)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var rejected *businessRequestError
			if errors.As(err, &rejected) && (rejected.Code == 409 || rejected.Code == 401) {
				return business.ErrDenied
			}
			if !businessPollWait(ctx) {
				return ctx.Err()
			}
			continue
		}
		if len(batch) == 0 {
			continue
		}
		if checkErr := c.stageBusinessBatch(ctx, lease, batch); checkErr != nil {
			if !businessPollWait(ctx) {
				return ctx.Err()
			}
			continue
		}
	}
	return ctx.Err()
}

// stageBusinessBatch applies all control fences before source events. Only a
// successful atomic cursor/ordinary-inbox stage releases generation candidates.
func (c *Channel) stageBusinessBatch(ctx context.Context, lease string, batch []json.RawMessage) error {
	if len(batch) > 100 {
		return business.ErrQuota
	}
	type admitted struct {
		update models.Update
		raw    json.RawMessage
		id     int64
	}
	updates := make([]admitted, 0, len(batch))
	next := int64(0)
	for rawIndex := range batch {
		raw := batch[rawIndex]
		var head struct {
			ID int64 `json:"update_id"`
		}
		if json.Unmarshal(raw, &head) != nil || head.ID <= 0 || head.ID == math.MaxInt64 {
			return business.ErrUnavailable
		}
		if head.ID >= next {
			next = head.ID + 1
		}
		if hasBusinessCandidateTag(raw) {
			continue
		} // Reserved local metadata can never originate at Telegram.
		update, err := decodeBusinessCompatibleUpdate(raw)
		if err != nil {
			continue
		}
		updates = append(updates, admitted{update: update, raw: raw, id: head.ID})
	}
	if next == 0 {
		return nil
	}
	sort.SliceStable(updates, func(i, j int) bool { return updates[i].id < updates[j].id })
	candidates := &businessCandidates{}
	work := context.WithValue(ctx, businessCandidatesKey{}, candidates)
	ordinary := make([]business.PollEntry, 0, len(updates))
	for phase := 0; phase < 3; phase++ {
		for entryIndex := range updates {
			entry := updates[entryIndex]
			priority := 2
			if entry.update.BusinessConnection != nil {
				priority = 0
			} else if entry.update.DeletedBusinessMessages != nil {
				priority = 1
			}
			if priority != phase {
				continue
			}
			handled, err := c.handleBusinessUpdate(work, &entry.update)
			if err != nil && !errors.Is(err, business.ErrDenied) && !errors.Is(err, business.ErrQuota) && !errors.Is(err, business.ErrStale) && !errors.Is(err, business.ErrUnsupported) {
				return err
			}
			if !handled {
				ordinary = append(ordinary, business.PollEntry{UpdateID: entry.id, Payload: entry.raw})
			}
		}
	}
	receipts := make([]business.PollEntry, 0, len(candidates.items))
	for candidateIndex := range candidates.items {
		candidate := candidates.items[candidateIndex]
		metadata, err := c.business.GetState(ctx, candidate.scope.OwnerID, candidate.scope.Key)
		if err != nil {
			if terminalBusinessCandidate(err) {
				continue
			}
			return err
		}
		peer := metadata.Peers[candidate.scope.PeerID]
		if peer == nil || peer.Revision == 0 || !peer.Policy.Enabled || !peer.Policy.Consent || !metadata.Connection.Policy.Enabled || !metadata.Connection.Policy.Consent {
			continue
		}
		candidate.expectedRevision = peer.Revision
		receipt, err := encodeBusinessCandidate(candidate)
		if err != nil {
			return err
		}
		receipts = append(receipts, receipt)
	}
	ordinary = append(ordinary, receipts...)
	entries := ordinary
	if checkErr := c.businessPoll.StageTelegramBusinessPoll(ctx, c.instanceID, c.botID, lease, next, entries, time.Now().UTC()); checkErr != nil {
		return checkErr
	}
	for receiptIndex := range receipts {
		receipt := receipts[receiptIndex]
		if _, err := c.drainBusinessCandidate(ctx, receipt); err != nil {
			return err
		}
	}
	return nil
}

func businessPollWait(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Adapt only the changed display DTO, without treating arbitrary nested URL,
// callback, wallet or transport metadata as prompt text.
func decodeBusinessCompatibleUpdate(raw []byte) (models.Update, error) {
	var value any
	if len(raw) > 1<<20 || json.Unmarshal(raw, &value) != nil {
		return models.Update{}, business.ErrDenied
	}
	nodes := 0
	var adapt func(any, int)
	adapt = func(v any, depth int) {
		nodes++
		if depth > 32 || nodes > 10000 {
			return
		}
		switch item := v.(type) {
		case []any:
			for childIndex := range item {
				child := item[childIndex]
				adapt(child, depth+1)
			}
		case map[string]any:
			if item["type"] == "expandable_blockquote" {
				if _, exists := item["blocks"]; exists {
					item["type"] = "blockquote"
				}
			}
			for childIndex := range item {
				child := item[childIndex]
				adapt(child, depth+1)
			}
		}
	}
	adapt(value, 0)
	encoded, err := json.Marshal(value)
	if err != nil {
		return models.Update{}, business.ErrDenied
	}
	var update models.Update
	err = json.Unmarshal(encoded, &update)
	return update, err
}
