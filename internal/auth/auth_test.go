package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newTestService(t *testing.T) (*Service, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("running migrations: %v", err)
	}

	svc := NewService(store, "test-secret-key", 0, 0)
	return svc, store
}

func TestLoginAndValidate(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	hash, err := HashPassword("mypassword")
	if err != nil {
		t.Fatal(err)
	}

	userID := uuid.Must(uuid.NewV7()).String()
	user := &domain.User{
		ID:           userID,
		Username:     "admin",
		PasswordHash: hash,
		Role:         domain.RoleAdmin,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	// Login with correct credentials.
	accessToken, refreshToken, mustChange, err := svc.Login(ctx, "admin", "mypassword")
	if err != nil {
		t.Fatal(err)
	}
	if accessToken == "" || refreshToken == "" {
		t.Fatal("expected non-empty tokens")
	}
	if mustChange {
		t.Error("expected mustChange=false")
	}

	// Validate access token.
	claims, err := svc.ValidateToken(accessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != userID {
		t.Errorf("expected userID %q, got %q", userID, claims.UserID)
	}
	if claims.Role != domain.RoleAdmin {
		t.Errorf("expected role admin, got %s", claims.Role)
	}

	// Wrong password.
	_, _, _, err = svc.Login(ctx, "admin", "wrongpassword")
	if err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}

	// Unknown user.
	_, _, _, err = svc.Login(ctx, "nobody", "password")
	if err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestRefreshToken(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	hash, _ := HashPassword("password")
	userID := uuid.Must(uuid.NewV7()).String()
	store.CreateUser(ctx, &domain.User{
		ID: userID, Username: "user1", PasswordHash: hash, Role: domain.RoleRegular,
	})

	_, refreshToken, _, err := svc.Login(ctx, "user1", "password")
	if err != nil {
		t.Fatal(err)
	}

	// Refresh.
	newAccess, err := svc.RefreshToken(ctx, refreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if newAccess == "" {
		t.Fatal("expected non-empty new access token")
	}

	// Validate new token.
	claims, err := svc.ValidateToken(newAccess)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != userID {
		t.Errorf("expected userID %q, got %q", userID, claims.UserID)
	}
}

func TestChangePassword(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	hash, _ := HashPassword("oldpass")
	userID := uuid.Must(uuid.NewV7()).String()
	store.CreateUser(ctx, &domain.User{
		ID: userID, Username: "user2", PasswordHash: hash, Role: domain.RoleRegular, MustChangePass: true,
	})

	// Change password.
	if err := svc.ChangePassword(ctx, userID, "oldpass", "newpass"); err != nil {
		t.Fatal(err)
	}

	// Old password should fail.
	_, _, _, err := svc.Login(ctx, "user2", "oldpass")
	if err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials with old password, got %v", err)
	}

	// New password should work.
	_, _, mustChange, err := svc.Login(ctx, "user2", "newpass")
	if err != nil {
		t.Fatal(err)
	}
	if mustChange {
		t.Error("expected mustChange=false after password change")
	}
}

func TestMustChangePassword(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	hash, _ := HashPassword("admin")
	userID := uuid.Must(uuid.NewV7()).String()
	store.CreateUser(ctx, &domain.User{
		ID: userID, Username: "admin", PasswordHash: hash, Role: domain.RoleAdmin, MustChangePass: true,
	})

	_, _, mustChange, err := svc.Login(ctx, "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !mustChange {
		t.Error("expected mustChange=true for new admin")
	}
}
