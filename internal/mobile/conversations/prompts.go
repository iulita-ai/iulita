package conversations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Choice identifies one allowed prompt response.
type Choice struct{ ID, Label string }

// ApprovalAuthority is a server-derived business snapshot, never a bearer token.
// Its representation deliberately matches the independent OAuth authority.
type ApprovalAuthority struct {
	PrincipalType, ServiceID, ServiceGrantID      string
	ClientEpoch                                   uint64
	Issuer, OwnerID, DeviceID, FamilyID, ClientID string
	OwnerEpoch, DeviceEpoch, SigningEpoch         uint64
	AuthTime                                      int64
	Scopes                                        []string
}

// Prompt records an expiring choice or action-confirmation request.
type Prompt struct {
	ExpectedFamily                                                                                                 string
	AllowText                                                                                                      bool
	ExpectedDevice                                                                                                 string
	MaxBytes, GrantedMaxBytes                                                                                      int64
	ID, OwnerID, ConversationID, TurnID, Kind, OperationID, Question, Preview, Nonce, State, Decision, ActorDevice string
	Generation, ProcessingEpoch                                                                                    uint64
	Choices                                                                                                        []Choice
	Fingerprint                                                                                                    []byte
	ExpiresAt                                                                                                      time.Time
	RequiredScopes                                                                                                 []string
	FreshAuthentication                                                                                            bool
	Approver                                                                                                       *ApprovalAuthority
}

func approvalPermits(p Prompt, a ApprovalAuthority) bool {
	if a.PrincipalType == "service" || a.OwnerID != p.OwnerID || !validID(a.DeviceID) || !validID(a.FamilyID) {
		return false
	}
	for _, required := range p.RequiredScopes {
		found := false
		for _, granted := range a.Scopes {
			if required == granted {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if p.FreshAuthentication && (a.AuthTime <= 0 || time.Unix(a.AuthTime, 0).Before(time.Now().Add(-5*time.Minute)) || time.Unix(a.AuthTime, 0).After(time.Now().Add(time.Second))) {
		return false
	}
	return true
}

func (r *Repository) promptNonce(p Prompt) string {
	nonce, _ := r.cursors.Seal("prompt.nonce.v1", struct{ ID, Owner, Turn string }{p.ID, p.OwnerID, p.TurnID}) //nolint:errcheck // The nonce payload contains only strings, which always JSON-encode.
	return nonce
}

// OpenPrompt persists an expiring prompt under the current execution fence.
func (r *Repository) OpenPrompt(ctx context.Context, f Fence, p Prompt) (Prompt, error) {
	if !utf8.ValidString(p.Question+p.Preview) || (p.Kind != "choice" && p.Kind != "confirmation") || len(p.Question) > 4096 || len(p.Preview) > 4096 || p.ExpiresAt.Before(time.Now()) || p.ExpiresAt.After(time.Now().Add(10*time.Minute)) || len(p.Choices) > 20 {
		return p, ErrInvalid
	}
	if p.MaxBytes < 0 || (p.Kind == "confirmation" && (!validID(p.OperationID) || len(p.Fingerprint) != 32 || p.AllowText)) {
		return p, ErrInvalid
	}
	if p.Kind == "choice" {
		if len(p.Choices) == 0 && !p.AllowText {
			return p, ErrInvalid
		}
		seen := map[string]bool{}
		for _, c := range p.Choices {
			if c.ID == "" || len(c.ID) > 128 || len(c.Label) > 1024 || seen[c.ID] {
				return p, ErrInvalid
			}
			seen[c.ID] = true
		}
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr2 := r.fence(ctx, tx, f); checkErr2 != nil {
		return p, checkErr2
	}
	p.ID = uuid.NewString()
	p.OwnerID = f.OwnerID
	p.ConversationID = f.ConversationID
	p.TurnID = f.TurnID
	p.Generation = f.Generation
	p.ProcessingEpoch = f.ProcessingEpoch
	p.State = "open"
	p.Nonce = ""
	p.Decision = ""
	p.ActorDevice = ""
	p.Approver = nil
	p.GrantedMaxBytes = 0
	nonce := r.promptNonce(p)
	hash := sha256.Sum256([]byte(nonce))
	body, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mobile_prompts(id,owner_id,conversation_id,turn_id,generation,processing_epoch,kind,operation_id,nonce_hash,fingerprint,state,body,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,'open',?,?)`, p.ID, p.OwnerID, p.ConversationID, p.TurnID, p.Generation, p.ProcessingEpoch, p.Kind, p.OperationID, hash[:], p.Fingerprint, body, p.ExpiresAt.UnixMilli())
	if err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET state='awaiting_prompt' WHERE owner_id=? AND conversation_id=? AND id=?`, p.OwnerID, p.ConversationID, p.TurnID); err != nil {
		return p, err
	}
	if checkErr3 := r.event(ctx, tx, p.OwnerID, p.ConversationID, p.Generation, p.TurnID, 0, p.ID, "prompt.changed"); checkErr3 != nil {
		return p, checkErr3
	}
	p.Nonce = nonce
	return p, tx.Commit()
}
func (r *Repository) readPrompt(ctx context.Context, tx *repoTx, owner, conversation, turn, id string) (Prompt, error) {
	var body []byte
	var p Prompt
	err := tx.QueryRowContext(ctx, `SELECT body,state,decision,actor_device FROM mobile_prompts WHERE id=? AND owner_id=? AND conversation_id=? AND turn_id=?`, id, owner, conversation, turn).Scan(&body, &p.State, &p.Decision, &p.ActorDevice)
	if err == sql.ErrNoRows {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	state, decision, actor := p.State, p.Decision, p.ActorDevice
	if json.Unmarshal(body, &p) != nil {
		return p, ErrInvalid
	}
	p.State = state
	p.Decision = decision
	p.ActorDevice = actor
	p.Nonce = r.promptNonce(p)
	return p, nil
}

// Prompt returns the owned prompt while enforcing its current lifetime.
func (r *Repository) Prompt(ctx context.Context, owner, conversation, turn, id string) (Prompt, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return Prompt{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if _, err = get(ctx, tx, owner, conversation); err != nil {
		return Prompt{}, err
	}
	p, err := r.readPrompt(ctx, tx, owner, conversation, turn, id)
	if err != nil {
		return p, err
	}
	if p.State == "open" && !p.ExpiresAt.After(time.Now()) {
		p.State = "expired"
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_prompts SET state='expired' WHERE id=? AND state='open'`, id); err != nil {
			return p, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET state='running' WHERE owner_id=? AND conversation_id=? AND id=? AND processing_epoch=? AND state='awaiting_prompt'`, owner, conversation, turn, p.ProcessingEpoch); err != nil {
			return p, err
		}
		if checkErr4 := r.event(ctx, tx, owner, conversation, p.Generation, turn, 0, id, "prompt.changed"); checkErr4 != nil {
			return p, checkErr4
		}
	}
	return p, tx.Commit()
}
func (r *Repository) prompts(ctx context.Context, tx *repoTx, owner, conversation string, generation uint64) ([]Prompt, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,turn_id FROM mobile_prompts WHERE owner_id=? AND conversation_id=? AND generation=? AND state='open' AND expires_at>? ORDER BY id`, owner, conversation, generation, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	type ref struct{ id, turn string }
	var refs []ref
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var x ref
		if err = rows.Scan(&x.id, &x.turn); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, err
		}
		refs = append(refs, x)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return nil, err
	}
	var out []Prompt
	for _, x := range refs {
		p, err := r.readPrompt(ctx, tx, owner, conversation, x.turn, x.id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Decide is out of band, CAS, and takes a server-verified device actor. Free text
// in SendMessage is never interpreted as a confirmation or native approval.
func (r *Repository) Decide(ctx context.Context, owner, device, conversation, turn, id string, generation uint64, decision, operation, nonce string, fingerprint []byte, authority ...ApprovalAuthority) (Prompt, error) {
	return r.DecideGranted(ctx, owner, device, conversation, turn, id, generation, decision, operation, nonce, fingerprint, 0, authority...)
}

// DecideGranted also binds a bounded byte grant to an HTTP confirmation.
func (r *Repository) DecideGranted(ctx context.Context, owner, device, conversation, turn, id string, generation uint64, decision, operation, nonce string, fingerprint []byte, granted int64, authority ...ApprovalAuthority) (Prompt, error) {
	if !validID(device) || !utf8.ValidString(decision) || len(decision) > 4096 {
		return Prompt{}, ErrInvalid
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return Prompt{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	c, err := get(ctx, tx, owner, conversation)
	if err != nil {
		return Prompt{}, err
	}
	if c.Generation != generation {
		return Prompt{}, ErrConflict
	}
	p, err := r.readPrompt(ctx, tx, owner, conversation, turn, id)
	if err != nil {
		return p, err
	}
	if p.Generation != generation || p.State != "open" || !p.ExpiresAt.After(time.Now()) {
		return p, ErrConflict
	}
	var live int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=? AND generation=? AND processing_epoch=? AND state='awaiting_prompt' AND cancellation_requested=0`, owner, conversation, turn, generation, p.ProcessingEpoch).Scan(&live); err != nil || live != 1 {
		return p, ErrFenced
	}
	if p.ExpectedFamily != "" && (len(authority) != 1 || authority[0].FamilyID != p.ExpectedFamily) {
		return p, ErrApproval
	}
	if p.ExpectedDevice != "" && p.ExpectedDevice != device {
		return p, ErrApproval
	}
	if granted < 0 || granted > p.MaxBytes || (p.Kind != "confirmation" && granted != 0) || (decision == "deny" && granted != 0) || (decision == "approve" && p.MaxBytes > 0 && granted == 0) {
		return p, ErrInvalid
	}
	p.GrantedMaxBytes = granted
	state := "answered"
	if p.Kind == "confirmation" {
		hash := sha256.Sum256([]byte(nonce))
		var stored []byte
		if checkErr5 := tx.QueryRowContext(ctx, `SELECT nonce_hash FROM mobile_prompts WHERE id=?`, id).Scan(&stored); checkErr5 != nil {
			return p, checkErr5
		}
		if operation != p.OperationID || !hmac.Equal(stored, hash[:]) || !hmac.Equal(fingerprint, p.Fingerprint) || (decision != "approve" && decision != "deny") {
			return p, ErrInvalid
		}
		if decision == "deny" {
			state = "rejected"
		} else {
			if len(p.RequiredScopes) > 0 || p.FreshAuthentication {
				if len(authority) != 1 || authority[0].DeviceID != device || !approvalPermits(p, authority[0]) {
					return p, ErrApproval
				}
			}
			if len(authority) == 1 {
				a := authority[0]
				a.Scopes = append([]string(nil), a.Scopes...)
				p.Approver = &a
			}
		}
	} else {
		found := p.AllowText && decision != "" && len(decision) <= 4096
		for _, c := range p.Choices {
			if c.ID == decision {
				found = true
			}
		}
		if !found || operation != "" || nonce != "" || len(fingerprint) > 0 {
			return p, ErrInvalid
		}
	}
	if p.Kind == "choice" && len(authority) == 1 {
		a := authority[0]
		if !approvalPermits(p, a) {
			return p, ErrApproval
		}
		p.Approver = &a
	}
	snapshotCopy := p
	snapshotCopy.Nonce = ""
	body, err := json.Marshal(snapshotCopy)
	if err != nil {
		return p, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE mobile_prompts SET state=?,decision=?,actor_device=?,body=? WHERE id=? AND owner_id=? AND state='open'`, state, decision, device, body, id, owner)
	if err != nil {
		return p, err
	}
	n, operationErr1 := res.RowsAffected()
	if operationErr1 != nil {
		return Prompt{}, operationErr1
	}
	if n != 1 {
		return p, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET state='running' WHERE owner_id=? AND conversation_id=? AND id=? AND processing_epoch=?`, owner, conversation, turn, p.ProcessingEpoch); err != nil {
		return p, err
	}
	if checkErr6 := r.event(ctx, tx, owner, conversation, generation, turn, 0, id, "prompt.changed"); checkErr6 != nil {
		return p, checkErr6
	}
	p.State = state
	p.Decision = decision
	p.ActorDevice = device
	return p, tx.Commit()
}
