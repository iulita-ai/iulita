// Package ephemeral keeps phone raw/derived content out of durable storage and
// backup. Ciphertexts and a per-process random key exist only in bounded RAM.
// A restart deliberately loses every body; metadata must never reconstruct it.
package ephemeral

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrExpired indicates that ephemeral content or its authority expired.
var ErrExpired = errors.New("mobile ephemeral result unavailable or expired")

// ErrQuota indicates that the content quota was reached.
var ErrQuota = errors.New("mobile ephemeral content quota exceeded")

// Binding binds stored state to its owner and authorization context.
type Binding struct{ Owner, Device, Task, Destination, ConsentRevision, Category string }
type entry struct {
	binding           Binding
	expiry            time.Time
	nonce, ciphertext []byte
	digest            [32]byte
}

// Store owns persistence or bounded content for this mobile subsystem.
type Store struct {
	mu        sync.Mutex
	aead      cipher.AEAD
	entries   map[string]entry
	bytes     int
	now       func() time.Time
	telemetry bool
}

// New constructs the subsystem with explicit dependencies and limits.
func New() (*Store, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	clear(key)
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{aead: aead, entries: map[string]entry{}, now: time.Now}, nil
}

// NewTelemetry separates passive history from command/result content and its
// shorter limits. Its random encryption key never leaves this process.
func NewTelemetry() (*Store, error) {
	s, err := New()
	if err != nil {
		return nil, err
	}
	s.telemetry = true
	return s, nil
}

func valid(b Binding) bool {
	for _, id := range []string{b.Owner, b.Device, b.Task} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return false
		}
	}
	return b.Destination != "" && b.ConsentRevision != "" && (b.Category == "status" || b.Category == "location" || b.Category == "notifications" || b.Category == "ui_tree" || b.Category == "derived" || b.Category == "command" || b.Category == "artifact")
}
func aad(b Binding) []byte {
	return []byte(b.Owner + "\x00" + b.Device + "\x00" + b.Task + "\x00" + b.Destination + "\x00" + b.ConsentRevision + "\x00" + b.Category)
}

// Put stores bounded content under its complete authorization binding.
func (s *Store) Put(id string, b Binding, body []byte, expiry time.Time) error {
	if !valid(b) || id == "" || len(body) == 0 || len(body) > 1<<20 {
		return ErrQuota
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	now := s.now()
	maxTTL := 15 * time.Minute
	maxEntries, maxBytes, maxOwner := 1024, 16<<20, 4<<20
	if s.telemetry {
		maxTTL = 24 * time.Hour
		maxEntries, maxBytes, maxOwner = 20000, 32<<20, 8<<20
		if b.ConsentRevision != "mobile-telemetry-v1" || !strings.HasPrefix(b.Destination, "mobile.telemetry:") || (b.Category != "status" && b.Category != "location" && b.Category != "notifications") {
			return ErrExpired
		}
	}
	if !expiry.After(now) || expiry.After(now.Add(maxTTL)) {
		return ErrExpired
	}
	digest := sha256.Sum256(body)
	if old, ok := s.entries[id]; ok {
		if old.binding == b && old.digest == digest && old.expiry.Equal(expiry) {
			return nil
		}
		return ErrExpired
	}
	if len(s.entries) >= maxEntries || s.bytes+len(body)+s.aead.Overhead() > maxBytes {
		return ErrQuota
	}
	ownerBytes := 0
	for _, e := range s.entries { //nolint:gocritic // Inspect a value snapshot while holding the store mutex.
		if e.binding.Owner == b.Owner {
			ownerBytes += len(e.ciphertext)
		}
	}
	if ownerBytes+len(body) > maxOwner {
		return ErrQuota
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ciphertext := s.aead.Seal(nil, nonce, body, aad(b))
	s.entries[id] = entry{b, expiry, nonce, ciphertext, digest}
	s.bytes += len(ciphertext)
	return nil
}

// Get returns the owned resource under its current access constraints.
func (s *Store) Get(id string, b Binding) ([]byte, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok || e.binding != b {
		return nil, time.Time{}, ErrExpired
	}
	if !e.expiry.After(s.now()) {
		s.erase(id)
		return nil, time.Time{}, ErrExpired
	}
	body, err := s.aead.Open(nil, e.nonce, e.ciphertext, aad(b))
	if err != nil {
		return nil, time.Time{}, ErrExpired
	}
	return body, e.expiry, nil
}
func (s *Store) erase(id string) {
	if e, ok := s.entries[id]; ok {
		s.bytes -= len(e.ciphertext)
		clear(e.ciphertext)
		clear(e.nonce)
		delete(s.entries, id)
	}
}
func (s *Store) cleanup() {
	now := s.now()
	for id, e := range s.entries { //nolint:gocritic // Inspect a value snapshot while holding the store mutex.
		if !e.expiry.After(now) {
			s.erase(id)
		}
	}
}

// DeleteTask erases all content belonging to the specified task.
func (s *Store) DeleteTask(owner, task string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.entries { //nolint:gocritic // Inspect a value snapshot while holding the store mutex.
		if e.binding.Owner == owner && e.binding.Task == task {
			s.erase(id)
		}
	}
}

// Delete erases the specified ephemeral content.
func (s *Store) Delete(id string, b Binding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok && e.binding == b {
		s.erase(id)
	}
}

// Run maintains expiry and cleanup until the context ends.
func (s *Store) Run(ctx context.Context) {
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for id := range s.entries {
				s.erase(id)
			}
			s.mu.Unlock()
			return
		case <-timer.C:
			s.mu.Lock()
			s.cleanup()
			s.mu.Unlock()
		}
	}
}

// Sweep erases expired ciphertext even while there are no uploads.
func (s *Store) Sweep() { s.mu.Lock(); defer s.mu.Unlock(); s.cleanup() }
