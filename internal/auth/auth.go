package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUserNotFound       = errors.New("user not found")
	ErrPasswordRequired   = errors.New("password change required")
)

// Claims represents the JWT payload.
type Claims struct {
	UserID   string          `json:"user_id"`
	Username string          `json:"username"`
	Role     domain.UserRole `json:"role"`
	jwt.RegisteredClaims
}

// Service handles authentication: JWT tokens, password hashing, user lookup.
type Service struct {
	store      storage.Repository
	jwtSecret  []byte
	tokenTTL   time.Duration
	refreshTTL time.Duration
}

// NewService creates an auth service. If jwtSecret is empty, a random one is generated.
func NewService(store storage.Repository, jwtSecret string, tokenExpiry, refreshExpiry time.Duration) *Service {
	secret := []byte(jwtSecret)
	if len(secret) == 0 {
		buf := make([]byte, 32)
		rand.Read(buf)
		secret = []byte(hex.EncodeToString(buf))
	}
	if tokenExpiry <= 0 {
		tokenExpiry = 24 * time.Hour
	}
	if refreshExpiry <= 0 {
		refreshExpiry = 7 * 24 * time.Hour
	}
	return &Service{
		store:      store,
		jwtSecret:  secret,
		tokenTTL:   tokenExpiry,
		refreshTTL: refreshExpiry,
	}
}

// Login validates credentials and returns an access token and refresh token.
func (s *Service) Login(ctx context.Context, username, password string) (accessToken, refreshToken string, mustChangePass bool, err error) {
	user, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		return "", "", false, fmt.Errorf("looking up user: %w", err)
	}
	if user == nil {
		return "", "", false, ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", "", false, ErrInvalidCredentials
	}

	accessToken, err = s.createToken(user, s.tokenTTL)
	if err != nil {
		return "", "", false, err
	}
	refreshToken, err = s.createToken(user, s.refreshTTL)
	if err != nil {
		return "", "", false, err
	}

	return accessToken, refreshToken, user.MustChangePass, nil
}

// RefreshToken validates a refresh token and returns a new access token.
func (s *Service) RefreshToken(ctx context.Context, tokenStr string) (string, error) {
	claims, err := s.ValidateToken(tokenStr)
	if err != nil {
		return "", fmt.Errorf("invalid refresh token: %w", err)
	}
	user, err := s.store.GetUser(ctx, claims.UserID)
	if err != nil || user == nil {
		return "", ErrUserNotFound
	}
	return s.createToken(user, s.tokenTTL)
}

// ValidateToken parses and validates a JWT token, returning the claims.
func (s *Service) ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}

// HashPassword creates a bcrypt hash of the password.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(hash), nil
}

// ChangePassword validates the old password and sets a new one.
func (s *Service) ChangePassword(ctx context.Context, userID, oldPass, newPass string) error {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil || user == nil {
		return ErrUserNotFound
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPass)); err != nil {
		return ErrInvalidCredentials
	}
	hash, err := HashPassword(newPass)
	if err != nil {
		return err
	}
	user.PasswordHash = hash
	user.MustChangePass = false
	return s.store.UpdateUser(ctx, user)
}

func (s *Service) createToken(user *domain.User, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   user.ID,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}
