package ephemeral

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestExpiryOwnershipCiphertextAndRestart(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	b := Binding{uuid.NewString(), uuid.NewString(), uuid.NewString(), "mobile.result", "consent-v1", "notifications"}
	body := []byte("CANARY_PRIVATE_NOTIFICATION_BODY")
	expiry := now.Add(time.Minute)
	if err = s.Put("observation", b, body, expiry); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.entries["observation"].ciphertext, body) {
		t.Fatal("plaintext body retained")
	}
	foreign := b
	foreign.Owner = uuid.NewString()
	if _, _, err = s.Get("observation", foreign); err == nil {
		t.Fatal("foreign body read")
	}
	got, _, err := s.Get("observation", b)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal(err)
	}
	now = expiry
	if _, _, err = s.Get("observation", b); !errors.Is(err, ErrExpired) || len(s.entries) != 0 {
		t.Fatal("read-time expiry failed", err)
	}
	s.now = time.Now
	if err = s.Put("next", b, body, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	restarted, _ := New()
	if _, _, err = restarted.Get("next", b); err == nil {
		t.Fatal("restart recovered old content")
	}
}
