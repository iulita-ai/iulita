package conversations

import "context"

// Admit snapshots server policy in the SAME acceptance transaction. A replay
// never re-resolves changed settings/provider policy or overwrites its snapshot.
func (r *Repository) Admit(ctx context.Context, owner, conversation string, in Input, policy func(Turn) ([]byte, error)) (Turn, bool, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return Turn{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	root := &repoRequest{repository: r, tx: tx.Tx}
	ownedRoot := root
	defer func() { ownedRoot.done = true }()
	if existing, ok := ctx.Value(transactionKey{}).(*repoRequest); ok {
		root = existing
	}
	t, replayed, err := r.Accept(context.WithValue(ctx, transactionKey{}, root), owner, conversation, in)
	if err != nil {
		return t, false, err
	}
	if !replayed {
		body, e := policy(t)
		if e != nil {
			return t, false, e
		}
		if len(body) > 64<<10 {
			return t, false, ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET policy_snapshot=? WHERE owner_id=? AND conversation_id=? AND id=?`, body, owner, conversation, t.ID); err != nil {
			return t, false, err
		}
	}
	return t, replayed, tx.Commit()
}

// Policy returns the immutable policy captured for the accepted turn.
func (r *Repository) Policy(ctx context.Context, f Fence) ([]byte, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr1 := r.fence(ctx, tx, f); checkErr1 != nil {
		return nil, checkErr1
	}
	var body []byte
	if checkErr2 := tx.QueryRowContext(ctx, `SELECT policy_snapshot FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=?`, f.OwnerID, f.ConversationID, f.TurnID).Scan(&body); checkErr2 != nil {
		return nil, checkErr2
	}
	return body, tx.Commit()
}
