package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func setupTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestInstalledSkillCRUD(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	// Save.
	sk := &domain.InstalledSkill{
		Slug:        "weather-brief",
		Name:        "Weather Brief",
		Version:     "1.0.0",
		Source:      "clawhub",
		SourceRef:   "clawhub/weather-brief",
		Isolation:   domain.IsolationTextOnly,
		InstallDir:  "/tmp/skills/weather-brief",
		Enabled:     true,
		Checksum:    "abc123",
		Description: "Get weather info",
		Author:      "test-author",
		Tags:        "weather,utility",
		InstalledAt: time.Now(),
	}
	if err := store.SaveInstalledSkill(ctx, sk); err != nil {
		t.Fatal(err)
	}

	// Get.
	got, err := store.GetInstalledSkill(ctx, "weather-brief")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Weather Brief" {
		t.Errorf("got name %q", got.Name)
	}
	if got.Version != "1.0.0" {
		t.Errorf("got version %q", got.Version)
	}
	if got.Isolation != domain.IsolationTextOnly {
		t.Errorf("got isolation %q", got.Isolation)
	}

	// List.
	all, err := store.ListInstalledSkills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d skills, want 1", len(all))
	}

	// Update.
	got.Version = "1.1.0"
	got.Enabled = false
	now := time.Now()
	got.UpdatedAt = &now
	if err := store.UpdateInstalledSkill(ctx, got); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetInstalledSkill(ctx, "weather-brief")
	if updated.Version != "1.1.0" {
		t.Errorf("got version %q after update", updated.Version)
	}
	if updated.Enabled {
		t.Error("should be disabled after update")
	}

	// Delete.
	if err := store.DeleteInstalledSkill(ctx, "weather-brief"); err != nil {
		t.Fatal(err)
	}
	all, _ = store.ListInstalledSkills(ctx)
	if len(all) != 0 {
		t.Errorf("got %d skills after delete, want 0", len(all))
	}
}

func TestInstalledSkillUpsert(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	sk := &domain.InstalledSkill{
		Slug:        "test-skill",
		Name:        "Test v1",
		Version:     "1.0.0",
		Source:      "url",
		Isolation:   domain.IsolationTextOnly,
		InstallDir:  "/tmp/skills/test",
		InstalledAt: time.Now(),
	}
	store.SaveInstalledSkill(ctx, sk)

	// Upsert with new version.
	sk.Name = "Test v2"
	sk.Version = "2.0.0"
	if err := store.SaveInstalledSkill(ctx, sk); err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetInstalledSkill(ctx, "test-skill")
	if got.Name != "Test v2" {
		t.Errorf("got name %q after upsert", got.Name)
	}
	if got.Version != "2.0.0" {
		t.Errorf("got version %q after upsert", got.Version)
	}

	// Should still be just one record.
	all, _ := store.ListInstalledSkills(ctx)
	if len(all) != 1 {
		t.Errorf("got %d skills, want 1 (upsert should not duplicate)", len(all))
	}
}

func TestGetInstalledSkillNotFound(t *testing.T) {
	store := setupTestStore(t)
	_, err := store.GetInstalledSkill(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}
