package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func TestAccountKnowledgeOwnershipAtomicJournalAndLegacyVisibility(t *testing.T) {
	r, path, owner, other, _ := fixture(t)
	c, err := r.Create(ctx, owner, "shared", Settings{ProfileID: "default", MemoryMode: "account"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Accept(ctx, owner, c.ID, input("memory")); err != nil {
		t.Fatal(err)
	}
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.ReleaseRuntime(ctx, rt)
	_, f, err := r.Claim(ctx, rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct{ chat, user, content string }{{"telegram:owner", owner, "canary account saved"}, {"telegram:other", other, "canary OTHER_PRIVATE"}, {"MoBiLe:shadow", owner, "canary PHONE_PRIVATE"}} {
		if _, err = r.db.ExecContext(ctx, `INSERT INTO facts(chat_id,user_id,content,source_type) VALUES(?,?,?,'user')`, seed.chat, seed.user, seed.content); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(name, id string, in KnowledgeInput) (string, error) {
		t.Helper()
		payload, _ := json.Marshal(in)
		if _, prepareErr := r.PrepareTool(ctx, f, id, "mutating", payload); prepareErr != nil {
			return "", prepareErr
		}
		if startErr := r.StartTool(ctx, f, id); startErr != nil {
			return "", startErr
		}
		return r.AccountKnowledge(ctx, f, id, name, in)
	}
	out, err := invoke("recall", "read", KnowledgeInput{Query: "canary"})
	if err != nil || !strings.Contains(out, "account saved") || strings.Contains(out, "PRIVATE") {
		t.Fatal("account isolation", out, err)
	}
	out, err = invoke("remember", "write", KnowledgeInput{Content: "Parity memory apple"})
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		ID int64 `json:"fact_id"`
	}
	if json.Unmarshal([]byte(out), &status) != nil || status.ID == 0 {
		t.Fatal(out)
	}
	replayed, err := r.AccountKnowledge(ctx, f, "write", "remember", KnowledgeInput{Content: "Parity memory apple"})
	if err != nil || replayed != out {
		t.Fatal("atomic replay", err)
	}
	if err = r.FinishTool(ctx, f, "write", "succeeded", out); err != nil {
		t.Fatal("atomic adapter finish", err)
	}
	if err = r.FinishTool(ctx, f, "write", "succeeded", "different"); err == nil {
		t.Fatal("rewrote atomic outcome")
	}
	old, err := legacy.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	facts, err := old.SearchFactsByUser(ctx, owner, "Parity", 10)
	if err != nil || len(facts) != 1 || facts[0].ID != status.ID {
		t.Fatal("not shared with Telegram memory", facts, err)
	}
	var foreignID int64
	if err = r.db.QueryRowContext(ctx, `SELECT id FROM facts WHERE user_id=?`, other).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke("forget", "foreign delete", KnowledgeInput{ID: foreignID}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM facts WHERE id=?`, foreignID).Scan(&count); err != nil || count != 1 {
		t.Fatal("foreign fact deleted", err)
	}
	if _, err = invoke("forget", "query delete", KnowledgeInput{Query: "canary"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("moving query deletion accepted", err)
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	facts, err = old.SearchFactsByUser(ctx, owner, "Parity", 10)
	if err != nil || len(facts) != 1 {
		t.Fatal("chat clear deleted shared memory", err)
	}
	if _, err = r.AccountKnowledge(ctx, f, "write", "remember", KnowledgeInput{Content: "late"}); !errors.Is(err, ErrFenced) {
		t.Fatal("stale fence wrote knowledge", err)
	}
}

func TestIsolatedKnowledgeAndRevokedVerifierNeverWrite(t *testing.T) {
	for _, mode := range []string{"isolated", "account"} {
		t.Run(mode, func(t *testing.T) {
			r, _, owner, _, _ := fixture(t)
			c, err := r.Create(ctx, owner, "test", Settings{ProfileID: "default", MemoryMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = r.Accept(ctx, owner, c.ID, input("remember")); err != nil {
				t.Fatal(err)
			}
			rt, err := r.AcquireRuntime(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer r.ReleaseRuntime(ctx, rt)
			_, f, err := r.Claim(ctx, rt, owner)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.PrepareTool(ctx, f, "write", "mutating", []byte("x")); err != nil {
				t.Fatal(err)
			}
			if err = r.StartTool(ctx, f, "write"); err != nil {
				t.Fatal(err)
			}
			callCtx := ctx
			if mode == "account" {
				callCtx = WithVerifier(ctx, func(context.Context) error { return ErrFenced })
			}
			if _, err = r.AccountKnowledge(callCtx, f, "write", "remember", KnowledgeInput{Content: "must never persist"}); err == nil {
				t.Fatal("forbidden memory write accepted")
			}
			var count int
			if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM facts`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed write left fact", count, err)
			}
		})
	}
}

func TestSessionSearchNeverJoinsOtherChatOwnerOrFutureTurn(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	first := create(t, r, owner)
	second := create(t, r, owner)
	foreign := create(t, r, other)
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.ReleaseRuntime(ctx, rt)
	for _, c := range []Conversation{first, second, foreign} {
		if _, _, err = r.Accept(ctx, c.OwnerID, c.ID, input("needle "+c.ID)); err != nil {
			t.Fatal(err)
		}
		_, f, claimErr := r.Claim(ctx, rt, c.OwnerID)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if err = r.Complete(ctx, f, "needle answer "+c.ID, "completed"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err = r.Accept(ctx, owner, first.ID, input("search")); err != nil {
		t.Fatal(err)
	}
	_, f, err := r.Claim(ctx, rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Accept(ctx, owner, first.ID, input("needle FUTURE_PRIVATE")); err != nil {
		t.Fatal(err)
	}
	out, err := r.SearchCurrentHistory(ctx, f, "needle", 10)
	if err != nil || !strings.Contains(out, first.ID) || strings.Contains(out, second.ID) || strings.Contains(out, foreign.ID) || strings.Contains(out, "FUTURE_PRIVATE") {
		t.Fatal("history isolation", out, err)
	}
}
