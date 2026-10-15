// Package securetoken supplies purpose-bound, authenticated opaque cursors.
package securetoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// ErrCursor rejects an invalid or expired signed cursor.
var ErrCursor = errors.New("invalid or stale mobile cursor")

// Signer authenticates bounded protocol cursors with a dedicated key.
type Signer struct{ key []byte }

// New constructs the subsystem with explicit dependencies and limits.
func New(key []byte) (*Signer, error) {
	if len(key) != 32 {
		return nil, errors.New("cursor key must have 256 bits")
	}
	return &Signer{key: append([]byte(nil), key...)}, nil
}

// Seal encodes and authenticates a bounded purpose-bound cursor.
func (s *Signer) Seal(purpose string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(purpose + "\x00" + body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Open verifies a cursor signature and decodes its purpose-bound payload.
func (s *Signer) Open(purpose, token string, v any) error {
	if len(token) > 4096 {
		return ErrCursor
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return ErrCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return ErrCursor
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(purpose + "\x00" + body))
	if !hmac.Equal(b, mac.Sum(nil)) {
		return ErrCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return ErrCursor
	}
	if err = json.Unmarshal(data, v); err != nil {
		return ErrCursor
	}
	return nil
}
