package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

func TestSkillProposalCRUD(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := &domain.SkillProposal{
		ChatID:          "chat1",
		UserID:          "user1",
		Slug:            "deploy-checklist",
		Name:            "Deploy Checklist",
		Description:     "Steps for a safe deploy",
		Body:            "Run health check after apply.",
		Triggers:        "deploy,rollout",
		Warnings:        "[]",
		SourceMessageID: 42,
	}
	if err := store.SaveSkillProposal(ctx, p); err != nil {
		t.Fatalf("save: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected ID to be populated")
	}
	if p.Status != domain.SkillProposalPending {
		t.Errorf("expected default status pending, got %q", p.Status)
	}

	// A rejected proposal too.
	rej := &domain.SkillProposal{ChatID: "chat1", Slug: "evil", Name: "Evil", Status: domain.SkillProposalRejected}
	if err := store.SaveSkillProposal(ctx, rej); err != nil {
		t.Fatalf("save rejected: %v", err)
	}

	// List all.
	all, err := store.ListSkillProposals(ctx, storage.SkillProposalFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 proposals, got %d", len(all))
	}

	// Filter by status.
	pending, err := store.ListSkillProposals(ctx, storage.SkillProposalFilter{Status: domain.SkillProposalPending})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Slug != "deploy-checklist" {
		t.Fatalf("expected 1 pending (deploy-checklist), got %+v", pending)
	}

	// Get by id.
	got, err := store.GetSkillProposal(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SourceMessageID != 42 || got.Triggers != "deploy,rollout" {
		t.Errorf("unexpected proposal: %+v", got)
	}

	// Update status (discard).
	if err := store.UpdateSkillProposalStatus(ctx, p.ID, domain.SkillProposalDiscarded); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = store.GetSkillProposal(ctx, p.ID)
	if got.Status != domain.SkillProposalDiscarded {
		t.Errorf("expected discarded, got %q", got.Status)
	}
}
