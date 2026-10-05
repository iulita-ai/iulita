package config

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

type overrideRepo struct{ rows []domain.ConfigOverride }

func (r *overrideRepo) GetConfigOverride(context.Context, string) (*domain.ConfigOverride, error) {
	return nil, nil
}
func (r *overrideRepo) ListConfigOverrides(context.Context) ([]domain.ConfigOverride, error) {
	return r.rows, nil
}
func (r *overrideRepo) SaveConfigOverride(context.Context, *domain.ConfigOverride) error { return nil }
func (r *overrideRepo) DeleteConfigOverride(context.Context, string) error               { return nil }

func TestStoreMasksLegacySecretsAndUnavailableEncryption(t *testing.T) {
	r := &overrideRepo{rows: []domain.ConfigOverride{
		{Key: "deepseek.api_key", Value: "synthetic-legacy-secret"},
		{Key: "encrypted", Value: "synthetic-ciphertext", Encrypted: true},
		{Key: "claude.model", Value: "model"},
	}}
	s := NewStore(nil, nil, r, nil, zap.NewNop())
	s.SetSecretKeys(map[string]bool{"deepseek.api_key": true})
	if err := s.LoadOverrides(context.Background()); err != nil {
		t.Fatal(err)
	}
	if value, ok := s.Get("encrypted"); ok || value != "" {
		t.Fatal("ciphertext returned as effective value")
	}
	for _, list := range [][]ConfigEntry{s.List(), s.ListDecrypted()} {
		for _, e := range list {
			switch e.Key {
			case "deepseek.api_key", "encrypted":
				if e.Value != "***" {
					t.Errorf("secret %s was not masked", e.Key)
				}
			case "claude.model":
				if e.Value != "model" {
					t.Error("ordinary value was masked")
				}
			}
		}
	}
}
