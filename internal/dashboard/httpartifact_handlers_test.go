package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

// fakeArtifacts is a minimal HTTPArtifactStore with one committed file.
type fakeArtifacts struct {
	root  string
	rows  map[string]*domain.HTTPArtifact
	files map[string][]byte
}

func newFakeArtifacts(t *testing.T) *fakeArtifacts {
	t.Helper()
	root := t.TempDir()
	fa := &fakeArtifacts{
		root:  root,
		rows:  make(map[string]*domain.HTTPArtifact),
		files: make(map[string][]byte),
	}
	fa.add("art_own", "u1", "report.bin", []byte("owner-bytes"))
	fa.add("art_other", "u2", "secret.bin", []byte("foreign-bytes"))
	return fa
}

func (f *fakeArtifacts) add(id, owner, name string, data []byte) {
	f.files[id] = data
	path := filepath.Join(f.root, id+".bin")
	_ = os.WriteFile(path, data, 0o600)
	f.rows[id] = &domain.HTTPArtifact{
		ID: id, OwnerUserID: owner, StorageName: id + ".bin", Filename: name,
		Size: int64(len(data)), State: domain.HTTPArtifactReady,
		Sensitive: true, ExpiresAt: time.Now().Add(time.Hour),
	}
}

func (f *fakeArtifacts) List(_ context.Context, owner string) ([]domain.HTTPArtifact, error) {
	var out []domain.HTTPArtifact
	for _, a := range f.rows {
		if a.OwnerUserID == owner && a.State == domain.HTTPArtifactReady {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeArtifacts) Get(_ context.Context, owner, id string) (*domain.HTTPArtifact, error) {
	a, ok := f.rows[id]
	if !ok || a.OwnerUserID != owner {
		return nil, os.ErrNotExist
	}
	return a, nil
}

func (f *fakeArtifacts) Delete(_ context.Context, owner, id string) error {
	if _, err := f.Get(context.Background(), owner, id); err != nil {
		return err
	}
	delete(f.rows, id)
	return nil
}

func (f *fakeArtifacts) Root() string { return f.root }

func newArtifactTestServer(t *testing.T) (*fiber.App, *fakeArtifacts, *ticketRegistry) {
	t.Helper()
	fa := newFakeArtifacts(t)
	s := &Server{httpArtifacts: fa, tickets: newTicketRegistry(), logger: zap.NewNop()}
	app := fiber.New()
	// Simulate the authenticated user u1.
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: "u1", Role: domain.RoleRegular})
		return c.Next()
	})
	api := app.Group("/api")
	api.Get("/http-artifacts", s.handleListHTTPArtifacts)
	api.Get("/http-artifacts/:id", s.handleGetHTTPArtifact)
	api.Delete("/http-artifacts/:id", s.handleDeleteHTTPArtifact)
	api.Post("/http-artifacts/:id/ticket", s.handleIssueDownloadTicket)
	api.Get("/http-artifacts/download/:ticket", s.handleDownloadByTicket)
	return app, fa, s.tickets
}

func TestArtifactEndpointsAreOwnerScoped(t *testing.T) {
	app, _, _ := newArtifactTestServer(t)

	// Listing shows only u1's artifact.
	req := newGetReq("/api/http-artifacts")
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&list)
	_ = res.Body.Close()
	if len(list) != 1 || list[0]["id"] != "art_own" {
		t.Fatalf("list = %+v", list)
	}

	// Foreign metadata is 404 (indistinguishable from missing).
	req = newGetReq("/api/http-artifacts/art_other")
	res, _ = app.Test(req, -1)
	_ = res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("foreign metadata status = %d", res.StatusCode)
	}

	// Foreign delete also 404.
	req = newBodyReq("DELETE", "/api/http-artifacts/art_other")
	res, _ = app.Test(req, -1)
	_ = res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("foreign delete status = %d", res.StatusCode)
	}
}

func TestDownloadTicketOneUseAndHeaders(t *testing.T) {
	app, _, _ := newArtifactTestServer(t)

	// Issue a ticket for the owner's artifact.
	req := newBodyReq("POST", "/api/http-artifacts/art_own/ticket")
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	var ticket struct {
		DownloadURL string `json:"download_url"`
	}
	if derr := json.NewDecoder(res.Body).Decode(&ticket); derr != nil {
		t.Fatal(derr)
	}
	_ = res.Body.Close()
	if ticket.DownloadURL == "" {
		t.Fatal("no ticket issued")
	}

	// First use: streams the stored bytes with the safe header set.
	req = newGetReq(ticket.DownloadURL)
	res, err = app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	buf := new(strings.Builder)
	copyBody(res.Body, buf)
	_ = res.Body.Close()
	if buf.String() != "owner-bytes" {
		t.Fatalf("download body = %q", buf.String())
	}
	for header, want := range map[string]string{
		"Content-Disposition":    `attachment; filename="report.bin"`,
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "private, no-store",
	} {
		if got := res.Header.Get(header); got != want {
			t.Fatalf("header %s = %q, want %q", header, got, want)
		}
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "application/octet-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	// Replay: the ticket is single-use.
	req = newGetReq(ticket.DownloadURL)
	res2, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != 410 {
		t.Fatalf("replay status = %d, want 410 Gone", res2.StatusCode)
	}

	// Unknown ticket is also 410.
	req = newGetReq("/api/http-artifacts/download/deadbeef")
	res3, _ := app.Test(req, -1)
	_ = res3.Body.Close()
	if res3.StatusCode != 410 {
		t.Fatalf("unknown ticket status = %d", res3.StatusCode)
	}
}

func copyBody(r interface{ Read(p []byte) (int, error) }, w *strings.Builder) int {
	buf := make([]byte, 4096)
	total := 0
	for {
		n, err := r.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			total += n
		}
		if err != nil {
			return total
		}
	}
}

func TestUnauthenticatedRequestsRejected(t *testing.T) {
	fa := newFakeArtifacts(t)
	s := &Server{httpArtifacts: fa, tickets: newTicketRegistry(), logger: zap.NewNop()}
	app := fiber.New()
	api := app.Group("/api")
	// NO claims injected: handlers must deny.
	api.Get("/http-artifacts", s.handleListHTTPArtifacts)
	api.Post("/http-artifacts/:id/ticket", s.handleIssueDownloadTicket)

	req := newGetReq("/api/http-artifacts")
	res, _ := app.Test(req, -1)
	_ = res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("unauthenticated list status = %d", res.StatusCode)
	}
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/http-artifacts/%s/ticket", "art_own"), http.NoBody)
	res2, _ := app.Test(req, -1)
	_ = res2.Body.Close()
	if res2.StatusCode != 401 {
		t.Fatalf("unauthenticated ticket status = %d", res2.StatusCode)
	}
}

func newGetReq(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, http.NoBody)
}

func newBodyReq(method, target string) *http.Request {
	return httptest.NewRequest(method, target, http.NoBody)
}
