package oauthstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func timeJWT(t time.Time) *jwt.NumericDate { return jwt.NewNumericDate(t) }
func jsonBytes(v any) ([]byte, error)      { return json.Marshal(v) }
func legacyToken(t *testing.T, c Claims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte("legacy-dashboard-secret"))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
