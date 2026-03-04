package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("running migrations: %v", err)
	}
	if err := store.CreateVectorTables(ctx); err != nil {
		t.Fatalf("creating vector tables: %v", err)
	}
	return store
}

func TestMessageCRUD(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Save messages.
	msg1 := &domain.ChatMessage{ChatID: "chat1", Role: domain.RoleUser, Content: "hello"}
	msg2 := &domain.ChatMessage{ChatID: "chat1", Role: domain.RoleAssistant, Content: "hi there"}
	if err := store.SaveMessage(ctx, msg1); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, msg2); err != nil {
		t.Fatal(err)
	}

	// Get history.
	history, err := store.GetHistory(ctx, "chat1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(history))
	}
	if history[0].Content != "hello" {
		t.Errorf("expected 'hello', got %q", history[0].Content)
	}

	// Clear history.
	if err := store.ClearHistory(ctx, "chat1"); err != nil {
		t.Fatal(err)
	}
	history, err = store.GetHistory(ctx, "chat1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("expected 0 messages after clear, got %d", len(history))
	}
}

func TestFactCRUD(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	fact := &domain.Fact{
		ChatID:  "chat1",
		Content: "The user likes Go programming",
	}
	if err := store.SaveFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if fact.ID == 0 {
		t.Fatal("expected fact to have an ID after save")
	}

	// Search via FTS.
	results, err := store.SearchFacts(ctx, "chat1", "Go programming", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected to find fact via FTS search")
	}

	// Get all facts.
	allFacts, err := store.GetAllFacts(ctx, "chat1")
	if err != nil {
		t.Fatal(err)
	}
	if len(allFacts) == 0 {
		t.Fatal("expected at least 1 fact")
	}
}

func TestTaskLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	task := &domain.Task{
		Type:        "test.task",
		Payload:     `{"key":"value"}`,
		Status:      domain.TaskStatusPending,
		ScheduledAt: time.Now(),
		MaxAttempts: 3,
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Claim.
	claimed, err := store.ClaimTask(ctx, "worker1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil {
		t.Fatal("expected to claim a task")
	}
	if claimed.WorkerID != "worker1" {
		t.Errorf("expected worker1, got %q", claimed.WorkerID)
	}

	// Start.
	if err := store.StartTask(ctx, claimed.ID, "worker1"); err != nil {
		t.Fatal(err)
	}

	// Complete.
	if err := store.CompleteTask(ctx, claimed.ID, "done"); err != nil {
		t.Fatal(err)
	}

	// Verify status.
	tasks, err := store.ListTasks(ctx, storage.TaskFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != domain.TaskStatusDone {
		t.Errorf("expected status done, got %s", tasks[0].Status)
	}
}

func TestOneShot_DeleteAfterRun(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	task := &domain.Task{
		Type:           "test.oneshot",
		Payload:        `{}`,
		Status:         domain.TaskStatusPending,
		ScheduledAt:    time.Now(),
		MaxAttempts:    1,
		OneShot:        true,
		DeleteAfterRun: true,
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimTask(ctx, "worker1", nil)
	if err != nil || claimed == nil {
		t.Fatal("expected to claim task")
	}
	if err := store.StartTask(ctx, claimed.ID, "worker1"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteTask(ctx, claimed.ID, "done"); err != nil {
		t.Fatal(err)
	}

	// Task should be deleted.
	tasks, err := store.ListTasks(ctx, storage.TaskFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected task to be deleted after run, got %d tasks", len(tasks))
	}
}

func TestEmbeddingCache(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	hash := "abc123"

	// Save.
	if err := store.SaveCachedEmbedding(ctx, hash, vec); err != nil {
		t.Fatal(err)
	}

	// Get.
	result, err := store.GetCachedEmbedding(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != len(vec) {
		t.Fatalf("expected %d dims, got %d", len(vec), len(result))
	}
	for i := range vec {
		if result[i] != vec[i] {
			t.Errorf("dim %d: expected %f, got %f", i, vec[i], result[i])
		}
	}

	// Miss — returns sql.ErrNoRows.
	result, err = store.GetCachedEmbedding(ctx, "nonexistent")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected nil for cache miss")
	}
}

func TestResponseCache(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	hash := "prompt_hash_123"
	model := "claude-sonnet"
	response := "Hello, world!"
	usageJSON := `{"input_tokens":10,"output_tokens":20}`

	// Save.
	if err := store.SaveCachedResponse(ctx, hash, model, response, usageJSON); err != nil {
		t.Fatal(err)
	}

	// Get (within TTL).
	cached, err := store.GetCachedResponse(ctx, hash, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil {
		t.Fatal("expected cache hit")
	}
	if cached.Response != response {
		t.Errorf("expected %q, got %q", response, cached.Response)
	}

	// Get (very short TTL — effectively expired).
	cached, err = store.GetCachedResponse(ctx, hash, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if cached != nil {
		t.Fatal("expected cache miss for expired TTL")
	}
}

func TestUsageWithCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.IncrementUsageWithCost(ctx, "chat1", 100, 50, 0.01); err != nil {
		t.Fatal(err)
	}
	if err := store.IncrementUsageWithCost(ctx, "chat1", 200, 100, 0.02); err != nil {
		t.Fatal(err)
	}

	cost, err := store.GetDailyCost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cost < 0.02 {
		t.Errorf("expected daily cost >= 0.02, got %f", cost)
	}
}

func TestBinaryVectorStorage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Save a fact first.
	fact := &domain.Fact{ChatID: "chat1", Content: "test vector fact"}
	if err := store.SaveFact(ctx, fact); err != nil {
		t.Fatal(err)
	}

	// Save vector (now binary BLOB).
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	if err := store.SaveFactVector(ctx, fact.ID, vec); err != nil {
		t.Fatal(err)
	}

	// Verify facts without embeddings doesn't include our fact.
	noEmb, err := store.FactsWithoutEmbeddings(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range noEmb {
		if f.ID == fact.ID {
			t.Fatal("fact should have embedding now")
		}
	}

	// FTS-only search should find it.
	results, err := store.SearchFacts(ctx, "chat1", "vector", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected to find fact via FTS search")
	}
}

// --- Multi-user tests ---

func TestUserCRUD(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	userID := uuid.Must(uuid.NewV7()).String()
	user := &domain.User{
		ID:           userID,
		Username:     "testuser",
		PasswordHash: "$2a$10$dummyhash",
		Role:         domain.RoleRegular,
		DisplayName:  "Test User",
		Timezone:     "Europe/Berlin",
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	// Get by ID.
	got, err := store.GetUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Username != "testuser" {
		t.Fatalf("expected user 'testuser', got %+v", got)
	}

	// Get by username.
	got, err = store.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != userID {
		t.Fatal("expected to find user by username")
	}

	// Update.
	got.DisplayName = "Updated Name"
	if err := store.UpdateUser(ctx, got); err != nil {
		t.Fatal(err)
	}
	got2, _ := store.GetUser(ctx, userID)
	if got2.DisplayName != "Updated Name" {
		t.Errorf("expected 'Updated Name', got %q", got2.DisplayName)
	}

	// List.
	users, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}

	// Delete.
	if err := store.DeleteUser(ctx, userID); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetUser(ctx, userID)
	if got != nil {
		t.Fatal("expected user to be deleted")
	}
}

func TestChannelBinding(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	userID := uuid.Must(uuid.NewV7()).String()
	user := &domain.User{
		ID:           userID,
		Username:     "channeluser",
		PasswordHash: "$2a$10$dummyhash",
		Role:         domain.RoleAdmin,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	// Bind a Telegram channel.
	ch := &domain.UserChannel{
		UserID:          userID,
		ChannelType:     "telegram",
		ChannelID:       "12345",
		ChannelUserID:   "67890",
		ChannelUsername: "tguser",
		Enabled:         true,
	}
	if err := store.BindChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if ch.ID == 0 {
		t.Fatal("expected channel binding to have an ID")
	}

	// Look up user by channel.
	found, err := store.GetUserByChannel(ctx, "telegram", "67890")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != userID {
		t.Fatal("expected to find user by channel binding")
	}

	// List channels.
	channels, err := store.ListUserChannels(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}

	// Unbind.
	if err := store.UnbindChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	channels, _ = store.ListUserChannels(ctx, userID)
	if len(channels) != 0 {
		t.Fatal("expected 0 channels after unbind")
	}
}

func TestUserScopedFacts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create two users.
	user1ID := uuid.Must(uuid.NewV7()).String()
	user2ID := uuid.Must(uuid.NewV7()).String()
	for _, u := range []*domain.User{
		{ID: user1ID, Username: "user1", PasswordHash: "x", Role: domain.RoleRegular},
		{ID: user2ID, Username: "user2", PasswordHash: "x", Role: domain.RoleRegular},
	} {
		if err := store.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	// Save facts for each user (different chats, same user).
	f1 := &domain.Fact{ChatID: "chatA", UserID: user1ID, Content: "user1 likes Golang"}
	f2 := &domain.Fact{ChatID: "chatB", UserID: user1ID, Content: "user1 uses VSCode"}
	f3 := &domain.Fact{ChatID: "chatC", UserID: user2ID, Content: "user2 prefers Python"}
	for _, f := range []*domain.Fact{f1, f2, f3} {
		if err := store.SaveFact(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	// User1 should see both their facts across channels.
	facts, err := store.GetAllFactsByUser(ctx, user1ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts for user1, got %d", len(facts))
	}

	// User2 should see only their facts.
	facts, err = store.GetAllFactsByUser(ctx, user2ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact for user2, got %d", len(facts))
	}

	// FTS search should be user-scoped.
	results, err := store.SearchFactsByUser(ctx, user1ID, "Golang", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result for user1, got %d", len(results))
	}

	// User2 shouldn't find user1's facts.
	results, err = store.SearchFactsByUser(ctx, user2ID, "Golang", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 search results for user2's Golang query, got %d", len(results))
	}

	// Recent facts by user.
	recent, err := store.GetRecentFactsByUser(ctx, user1ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent facts for user1, got %d", len(recent))
	}
}

func TestUserScopedInsights(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	user1ID := uuid.Must(uuid.NewV7()).String()
	user2ID := uuid.Must(uuid.NewV7()).String()
	for _, u := range []*domain.User{
		{ID: user1ID, Username: "user1", PasswordHash: "x", Role: domain.RoleRegular},
		{ID: user2ID, Username: "user2", PasswordHash: "x", Role: domain.RoleRegular},
	} {
		if err := store.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	i1 := &domain.Insight{ChatID: "chatA", UserID: user1ID, Content: "user1 insight about patterns", FactIDs: "1,2"}
	i2 := &domain.Insight{ChatID: "chatB", UserID: user2ID, Content: "user2 insight about design", FactIDs: "3"}
	for _, i := range []*domain.Insight{i1, i2} {
		if err := store.SaveInsight(ctx, i); err != nil {
			t.Fatal(err)
		}
	}

	// User1 should only see their insights.
	insights, err := store.GetRecentInsightsByUser(ctx, user1ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) != 1 {
		t.Fatalf("expected 1 insight for user1, got %d", len(insights))
	}

	// Count.
	count, err := store.CountInsightsByUser(ctx, user1ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected count 1 for user1, got %d", count)
	}
}

func TestUserScopedDirective(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	userID := uuid.Must(uuid.NewV7()).String()
	store.CreateUser(ctx, &domain.User{ID: userID, Username: "diruser", PasswordHash: "x", Role: domain.RoleRegular})

	d := &domain.Directive{
		ChatID:  "chat1",
		UserID:  userID,
		Content: "Always respond in Russian",
	}
	if err := store.SaveDirective(ctx, d); err != nil {
		t.Fatal(err)
	}

	// Get by user.
	got, err := store.GetDirectiveByUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Content != "Always respond in Russian" {
		t.Fatalf("expected directive content, got %+v", got)
	}
}

func TestUserScopedTechFacts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	user1ID := uuid.Must(uuid.NewV7()).String()
	user2ID := uuid.Must(uuid.NewV7()).String()
	for _, u := range []*domain.User{
		{ID: user1ID, Username: "u1", PasswordHash: "x", Role: domain.RoleRegular},
		{ID: user2ID, Username: "u2", PasswordHash: "x", Role: domain.RoleRegular},
	} {
		store.CreateUser(ctx, u)
	}

	// Upsert tech facts with user_id set.
	tf1 := &domain.TechFact{ChatID: "chatA", UserID: user1ID, Category: "language", Key: "primary", Value: "Go", Confidence: 0.9}
	tf2 := &domain.TechFact{ChatID: "chatB", UserID: user2ID, Category: "language", Key: "primary", Value: "Python", Confidence: 0.8}
	for _, tf := range []*domain.TechFact{tf1, tf2} {
		if err := store.UpsertTechFact(ctx, tf); err != nil {
			t.Fatal(err)
		}
	}

	// Get by user.
	facts, err := store.GetTechFactsByUser(ctx, user1ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 tech fact for user1, got %d", len(facts))
	}

	// Count by user.
	count, err := store.CountTechFactsByUser(ctx, user1ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected count 1 for user1, got %d", count)
	}

	// Get by category and user.
	catFacts, err := store.GetTechFactsByCategoryAndUser(ctx, user1ID, "language")
	if err != nil {
		t.Fatal(err)
	}
	if len(catFacts) != 1 {
		t.Fatalf("expected 1 tech fact in language category for user1, got %d", len(catFacts))
	}
}
