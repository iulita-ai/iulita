package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

// HTTPArtifactStore is the delivery-side view of the artifact store.
type HTTPArtifactStore interface {
	List(ctx context.Context, ownerUserID string) ([]domain.HTTPArtifact, error)
	Get(ctx context.Context, ownerUserID, id string) (*domain.HTTPArtifact, error)
	Delete(ctx context.Context, ownerUserID, id string) error
	Root() string
}

// downloadTicket is a one-time browser download capability: hashed in memory
// only, TTL 60s, bound to one artifact and its owner. Issued exclusively via
// the authenticated API; consumed by browser navigation (plan §9.3).
type downloadTicket struct {
	hash      [32]byte
	artifact  string
	owner     string
	expiresAt time.Time
}

type ticketRegistry struct {
	mu      sync.Mutex
	tickets map[[32]byte]*downloadTicket
}

func newTicketRegistry() *ticketRegistry {
	return &ticketRegistry{tickets: make(map[[32]byte]*downloadTicket)}
}

const downloadTicketTTL = 60 * time.Second

func (tr *ticketRegistry) issue(artifact, owner string) (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("ticket entropy: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	t := &downloadTicket{
		hash:      sha256.Sum256([]byte(token)),
		artifact:  artifact,
		owner:     owner,
		expiresAt: time.Now().Add(downloadTicketTTL),
	}
	tr.mu.Lock()
	// Opportunistic expiry sweep keeps the map tiny.
	for k, v := range tr.tickets {
		if v.expiresAt.Before(time.Now()) {
			delete(tr.tickets, k)
		}
	}
	tr.tickets[t.hash] = t
	tr.mu.Unlock()
	return token, nil
}

// consume atomically removes the ticket; replay answers nothing.
func (tr *ticketRegistry) consume(token string) *downloadTicket {
	sum := sha256.Sum256([]byte(token))
	tr.mu.Lock()
	defer tr.mu.Unlock()
	t, ok := tr.tickets[sum]
	if !ok {
		return nil
	}
	delete(tr.tickets, sum)
	if t.expiresAt.Before(time.Now()) {
		return nil
	}
	return t
}

// handleListHTTPArtifacts returns the caller's artifacts (owner-scoped).
func (s *Server) handleListHTTPArtifacts(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" {
		return fiber.ErrUnauthorized
	}
	list, err := s.httpArtifacts.List(c.Context(), claims.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "listing artifacts failed"})
	}
	type view struct {
		ID        string    `json:"id"`
		Filename  string    `json:"filename"`
		Bytes     int64     `json:"bytes"`
		SHA256    string    `json:"sha256"`
		MIME      string    `json:"mime,omitempty"`
		Sensitive bool      `json:"sensitive"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	out := make([]view, 0, len(list))
	for i := range list {
		m := &list[i]
		if m.State != domain.HTTPArtifactReady {
			continue
		}
		out = append(out, view{
			ID: m.ID, Filename: m.Filename, Bytes: m.Size, SHA256: m.SHA256,
			MIME: m.MIME, Sensitive: m.Sensitive, ExpiresAt: m.ExpiresAt,
		})
	}
	return c.JSON(out)
}

// handleGetHTTPArtifact returns one artifact's metadata.
func (s *Server) handleGetHTTPArtifact(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" {
		return fiber.ErrUnauthorized
	}
	m, err := s.httpArtifacts.Get(c.Context(), claims.UserID, c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound // foreign and missing are indistinguishable
	}
	return c.JSON(fiber.Map{
		"id": m.ID, "filename": m.Filename, "bytes": m.Size,
		"sha256": m.SHA256, "mime": m.MIME, "sensitive": m.Sensitive,
		"expires_at": m.ExpiresAt,
	})
}

// handleDeleteHTTPArtifact removes an owner's artifact.
func (s *Server) handleDeleteHTTPArtifact(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" {
		return fiber.ErrUnauthorized
	}
	if err := s.httpArtifacts.Delete(c.Context(), claims.UserID, c.Params("id")); err != nil {
		return fiber.ErrNotFound
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// handleIssueDownloadTicket creates a one-time download capability.
func (s *Server) handleIssueDownloadTicket(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" {
		return fiber.ErrUnauthorized
	}
	id := c.Params("id")
	if _, err := s.httpArtifacts.Get(c.Context(), claims.UserID, id); err != nil {
		return fiber.ErrNotFound
	}
	token, err := s.tickets.issue(id, claims.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "ticket issuance failed"})
	}
	return c.JSON(fiber.Map{
		"download_url": "/api/http-artifacts/download/" + token,
		"expires_in":   int(downloadTicketTTL.Seconds()),
	})
}

// handleDownloadByTicket streams the artifact for a valid one-time ticket.
// The ticket IS the authorization (browser navigation cannot carry headers);
// it grants exactly one artifact of its owner and nothing else. Serving
// response: attachment + nosniff + octet-stream + no-store, and the ORIGIN
// Content-Encoding is deliberately NOT copied — an opaque gzip file must not
// be transparently decompressed by the browser (hash/size would mismatch).
func (s *Server) handleDownloadByTicket(c *fiber.Ctx) error {
	t := s.tickets.consume(c.Params("ticket"))
	if t == nil {
		return c.Status(fiber.StatusGone).JSON(fiber.Map{"error": "ticket expired or already used"})
	}
	m, err := s.httpArtifacts.Get(c.Context(), t.owner, t.artifact)
	if err != nil || m.State != domain.HTTPArtifactReady {
		return fiber.ErrNotFound
	}
	path := filepath.Join(s.httpArtifacts.Root(), m.StorageName)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return fiber.ErrNotFound
	}

	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", safeDownloadName(m.Filename)))
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set(fiber.HeaderContentType, "application/octet-stream")
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	c.Set("Referrer-Policy", "no-referrer")
	c.Set(fiber.HeaderContentLength, fmt.Sprintf("%d", st.Size()))
	// SendFile streams from disk inside the handler; fasthttp releases the
	// descriptor when the transfer (or disconnect) completes. Compression is
	// disabled: the stored bytes are the hash-verified representation.
	return c.SendFile(path, false)
}

func safeDownloadName(name string) string {
	var b []rune
	for _, r := range name {
		if r < 0x20 || r == '"' || r == '\\' {
			continue
		}
		b = append(b, r)
	}
	if len(b) == 0 {
		return "download"
	}
	return string(b)
}
