package conversations

import (
	"context"
	"encoding/json"
)

// Existing empty-target turns remain caller-device only. A model argument or
// newer token can never reinterpret an explicit target saved with the turn.
var targetSchema = []string{
	`ALTER TABLE mobile_turns ADD COLUMN target_device_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE mobile_chat_schema RENAME TO mobile_chat_schema_v3`,
	`CREATE TABLE mobile_chat_schema(version INTEGER PRIMARY KEY CHECK(version=4))`,
	`INSERT INTO mobile_chat_schema VALUES(4)`,
	`DROP TABLE mobile_chat_schema_v3`,
}

// PhoneTargetAllowed checks the immutable target on the accepted, live origin.
// Its caller must also validate current account/device/family authority.
func (r *Repository) PhoneTargetAllowed(ctx context.Context, owner, conversation, turn string, generation uint64, device string) error {
	if !validID(device) || r.OriginAllowed(ctx, owner, conversation, turn, generation) != nil {
		return ErrFenced
	}
	var target string
	var policy []byte
	if err := r.authorityReads.QueryRowContext(ctx, `SELECT target_device_id,policy_snapshot FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=? AND generation=?`, owner, conversation, turn, generation).Scan(&target, &policy); err != nil {
		return ErrFenced
	}
	if target == "" {
		var saved struct{ Authority struct{ DeviceID string } }
		if json.Unmarshal(policy, &saved) != nil {
			return ErrFenced
		}
		target = saved.Authority.DeviceID
	}
	if target == "" || target != device {
		return ErrFenced
	}
	return nil
}
