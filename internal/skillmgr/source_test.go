package skillmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClawhHubSourceResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills/weather-brief" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(clawhubSkillDetail{
			Skill: struct {
				Slug        string `json:"slug"`
				DisplayName string `json:"displayName"`
				Summary     string `json:"summary"`
			}{
				Slug:        "weather-brief",
				DisplayName: "Weather Brief",
				Summary:     "Get weather info",
			},
			LatestVersion: struct {
				Version string `json:"version"`
			}{Version: "1.0.0"},
			Owner: struct {
				Handle      string `json:"handle"`
				DisplayName string `json:"displayName"`
			}{Handle: "test-author"},
		})
	}))
	defer srv.Close()

	src := NewClawhHubSource("test-token", srv.Client(), nil)
	src.baseURL = srv.URL + "/api/v1"

	ref, err := src.Resolve(context.Background(), "weather-brief")
	if err != nil {
		t.Fatal(err)
	}

	if ref.Slug != "weather-brief" {
		t.Errorf("got slug %q", ref.Slug)
	}
	if ref.Name != "Weather Brief" {
		t.Errorf("got name %q", ref.Name)
	}
	if ref.Version != "1.0.0" {
		t.Errorf("got version %q", ref.Version)
	}
	if ref.Source != "clawhub" {
		t.Errorf("got source %q", ref.Source)
	}
	if ref.DownloadURL == "" {
		t.Error("expected non-empty download URL")
	}
}

func TestClawhHubSourceResolveWithPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(clawhubSkillDetail{
			Skill: struct {
				Slug        string `json:"slug"`
				DisplayName string `json:"displayName"`
				Summary     string `json:"summary"`
			}{Slug: "test", DisplayName: "Test"},
		})
	}))
	defer srv.Close()

	src := NewClawhHubSource("", srv.Client(), nil)
	src.baseURL = srv.URL + "/api/v1"

	ref, err := src.Resolve(context.Background(), "clawhub/test")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Slug != "test" {
		t.Errorf("got slug %q, want %q", ref.Slug, "test")
	}
}

func TestClawhHubSourceSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Search endpoint is /search, not /skills
		if !strings.HasSuffix(r.URL.Path, "/search") {
			t.Errorf("got path %q, want /api/v1/search", r.URL.Path)
		}
		q := r.URL.Query().Get("q")
		if q != "weather" {
			t.Errorf("got query %q, want 'weather'", q)
		}
		json.NewEncoder(w).Encode(clawhubSearchResponse{
			Results: []clawhubSearchResult{
				{Score: 3.8, Slug: "weather-brief", DisplayName: "Weather Brief", Summary: "Get weather"},
				{Score: 3.5, Slug: "weather-full", DisplayName: "Full Weather", Summary: "Full forecast"},
			},
		})
	}))
	defer srv.Close()

	src := NewClawhHubSource("", srv.Client(), nil)
	src.baseURL = srv.URL + "/api/v1"

	results, err := src.Search(context.Background(), "weather", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Slug != "weather-brief" {
		t.Errorf("got first slug %q", results[0].Slug)
	}
	if results[0].Name != "Weather Brief" {
		t.Errorf("got first name %q, want 'Weather Brief'", results[0].Name)
	}
	if results[0].Description != "Get weather" {
		t.Errorf("got first description %q", results[0].Description)
	}
	if results[0].DownloadURL == "" {
		t.Error("expected non-empty download URL")
	}
}

func TestClawhHubSourceResolve404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	src := NewClawhHubSource("", srv.Client(), nil)
	src.baseURL = srv.URL + "/api/v1"

	_, err := src.Resolve(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestClawhHubSourceAuthorization(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(clawhubSkillDetail{
			Skill: struct {
				Slug        string `json:"slug"`
				DisplayName string `json:"displayName"`
				Summary     string `json:"summary"`
			}{Slug: "test"},
		})
	}))
	defer srv.Close()

	src := NewClawhHubSource("my-token", srv.Client(), nil)
	src.baseURL = srv.URL + "/api/v1"

	src.Resolve(context.Background(), "test")
	if gotAuth != "Bearer my-token" {
		t.Errorf("got auth %q, want 'Bearer my-token'", gotAuth)
	}
}

func TestClawhHubExtractSlugFromRef(t *testing.T) {
	src := NewClawhHubSource("", nil, nil)
	tests := []struct {
		ref  string
		want string
	}{
		{"weather", "weather"},
		{"clawhub/weather", "weather"},
		{"steipete/weather", "weather"},
		{"https://clawhub.ai/steipete/weather", "weather"},
		{"https://clawhub.ai/owner/my-skill", "my-skill"},
		{"http://clawhub.ai/owner/skill", "skill"}, // http falls through to owner/slug parsing
	}
	for _, tt := range tests {
		got := src.extractSlugFromRef(tt.ref)
		if got != tt.want {
			t.Errorf("extractSlugFromRef(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestClawhHubIsClawhHubURL(t *testing.T) {
	if !IsClawhHubURL("https://clawhub.ai/steipete/weather") {
		t.Error("should recognize https://clawhub.ai/...")
	}
	if IsClawhHubURL("http://clawhub.ai/steipete/weather") {
		t.Error("should NOT recognize http:// (HTTPS only)")
	}
	if IsClawhHubURL("https://example.com/weather.zip") {
		t.Error("should not match example.com")
	}
}

func TestClawhHubDownloadURL(t *testing.T) {
	src := NewClawhHubSource("", nil, nil)
	url := src.downloadURL("weather")
	want := "https://clawhub.ai/api/v1/download?slug=weather"
	if url != want {
		t.Errorf("downloadURL = %q, want %q", url, want)
	}
}

func TestClawhHubRetryOn429(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("Rate limit exceeded"))
			return
		}
		json.NewEncoder(w).Encode(clawhubSkillDetail{
			Skill: struct {
				Slug        string `json:"slug"`
				DisplayName string `json:"displayName"`
				Summary     string `json:"summary"`
			}{Slug: "test", DisplayName: "Test"},
		})
	}))
	defer srv.Close()

	src := NewClawhHubSource("", srv.Client(), nil)
	src.baseURL = srv.URL + "/api/v1"

	ref, err := src.Resolve(context.Background(), "test")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if ref.Slug != "test" {
		t.Errorf("got slug %q, want %q", ref.Slug, "test")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestClawhHubRetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("Rate limit exceeded"))
	}))
	defer srv.Close()

	src := NewClawhHubSource("", srv.Client(), nil)
	src.baseURL = srv.URL + "/api/v1"

	_, err := src.Resolve(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("expected rate limit error, got: %v", err)
	}
}

func TestClawhHubRetryContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	src := NewClawhHubSource("", srv.Client(), nil)
	src.baseURL = srv.URL + "/api/v1"

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := src.Resolve(ctx, "test")
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestURLSourceResolve(t *testing.T) {
	src := NewURLSource(nil)

	ref, err := src.Resolve(context.Background(), "https://example.com/skills/weather-brief.zip")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Slug != "weather-brief" {
		t.Errorf("got slug %q", ref.Slug)
	}
	if ref.Source != "url" {
		t.Errorf("got source %q", ref.Source)
	}
}

func TestURLSourceResolveQueryParam(t *testing.T) {
	src := NewURLSource(nil)

	ref, err := src.Resolve(context.Background(), "https://example.com/api/v1/download?slug=weather")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Slug != "weather" {
		t.Errorf("got slug %q, want 'weather'", ref.Slug)
	}
}

func TestURLSourceResolveInvalid(t *testing.T) {
	src := NewURLSource(nil)
	_, err := src.Resolve(context.Background(), "not-a-url")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestURLSourceRejectHTTP(t *testing.T) {
	src := NewURLSource(nil)
	_, err := src.Resolve(context.Background(), "http://example.com/skill.zip")
	if err == nil {
		t.Fatal("expected error for HTTP URL (HTTPS required)")
	}
}

func TestURLSourceSearch(t *testing.T) {
	src := NewURLSource(nil)
	_, err := src.Search(context.Background(), "test", 10)
	if err == nil {
		t.Fatal("URL source should not support search")
	}
}

func TestDeriveSlugFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://example.com/skills/weather-brief.zip", "weather-brief"},
		{"https://example.com/api/v1/download?slug=weather", "weather"},
		{"https://example.com/api/v1/download?name=my-skill&format=zip", "my-skill"},
		{"https://github.com/user/repo/archive/refs/heads/main.zip", "main"},
		{"https://example.com/skill.zip", "skill"},
		{"https://example.com/my-repo-main.zip", "my-repo"},
		{"https://example.com/my-repo-master.zip", "my-repo"},
	}
	for _, tt := range tests {
		got := deriveSlugFromURL(tt.url)
		if got != tt.want {
			t.Errorf("deriveSlugFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestLocalSourceResolve(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: test\n---\nHello"), 0644)

	src := NewLocalSource()
	ref, err := src.Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	if ref.Source != "local" {
		t.Errorf("got source %q", ref.Source)
	}
	if ref.SourceRef != dir {
		t.Errorf("got source_ref %q", ref.SourceRef)
	}
}

func TestLocalSourceResolveNoSkillMD(t *testing.T) {
	dir := t.TempDir()
	src := NewLocalSource()
	_, err := src.Resolve(context.Background(), dir)
	if err == nil {
		t.Fatal("expected error for missing SKILL.md")
	}
}

func TestLocalSourceResolveNonexistent(t *testing.T) {
	src := NewLocalSource()
	_, err := src.Resolve(context.Background(), "/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestLocalSourceSearch(t *testing.T) {
	src := NewLocalSource()
	_, err := src.Search(context.Background(), "test", 10)
	if err == nil {
		t.Fatal("local source should not support search")
	}
}
