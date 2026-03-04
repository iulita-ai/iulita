package webfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestSkill creates a webfetch Skill for testing with SSRF URL check disabled
// (httptest servers bind to 127.0.0.1 which is blocked by CheckURLSSRF).
func newTestSkill(client *http.Client) *Skill {
	s := New(client)
	s.skipURLCheck = true
	return s
}

func TestSkillMeta(t *testing.T) {
	s := New(nil)
	if s.Name() != "webfetch" {
		t.Fatalf("expected name webfetch, got %s", s.Name())
	}
	if s.Description() == "" {
		t.Fatal("description should not be empty")
	}
	var schema map[string]any
	if err := json.Unmarshal(s.InputSchema(), &schema); err != nil {
		t.Fatalf("invalid schema JSON: %v", err)
	}
	caps := s.RequiredCapabilities()
	if len(caps) != 1 || caps[0] != "web" {
		t.Fatalf("expected [web], got %v", caps)
	}
}

func TestExecute_EmptyURL(t *testing.T) {
	s := New(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"url":""}`))
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("expected 'url is required' error, got: %v", err)
	}
}

func TestExecute_InvalidInput(t *testing.T) {
	s := New(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil || !strings.Contains(err.Error(), "invalid input") {
		t.Fatalf("expected 'invalid input' error, got: %v", err)
	}
}

func TestExecute_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := newTestSkill(srv.Client())
	input, _ := json.Marshal(map[string]string{"url": srv.URL})
	_, err := s.Execute(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got: %v", err)
	}
}

const testHTML = `<!DOCTYPE html>
<html>
<head><title>Test Article</title></head>
<body>
<article>
<h1>Test Article</h1>
<p>This is the main content of the test article. It contains enough text to be
recognized by the readability algorithm as meaningful content that should be
extracted and returned to the caller. We need several sentences to make this work
properly, so here is some more text to fill out the article body.</p>
<p>Second paragraph with additional content. The readability parser needs a
reasonable amount of text to determine what the main content area is. This helps
ensure that advertisements, navigation, and other non-content elements are
properly filtered out from the final result.</p>
</article>
</body>
</html>`

func TestExecute_HTMLArticle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(testHTML))
	}))
	defer srv.Close()

	// Use srv.Client() to allow localhost connections (bypasses SSRF protection).
	s := newTestSkill(srv.Client())
	input, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, err := s.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "<<<EXTERNAL_CONTENT") {
		t.Error("result should contain EXTERNAL_CONTENT marker")
	}
	if !strings.Contains(result, "<<<END_EXTERNAL_CONTENT>>>") {
		t.Error("result should contain END_EXTERNAL_CONTENT marker")
	}
	if !strings.Contains(result, "main content") {
		t.Error("result should contain article text")
	}
}

func TestExecute_PlainTextFallback(t *testing.T) {
	// Serve something that readability can't parse as article — plain text.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><p>tiny</p></body></html>`))
	}))
	defer srv.Close()

	s := newTestSkill(srv.Client())
	input, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, err := s.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still return something (either parsed or fallback).
	if !strings.Contains(result, "<<<EXTERNAL_CONTENT") {
		t.Error("result should contain EXTERNAL_CONTENT marker even for fallback")
	}
}

func TestExecute_Truncation(t *testing.T) {
	// Generate large HTML content.
	bigParagraph := strings.Repeat("This is a long sentence for testing truncation purposes. ", 500)
	html := `<!DOCTYPE html><html><head><title>Big</title></head><body><article><h1>Big Article</h1><p>` +
		bigParagraph + `</p></article></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	}))
	defer srv.Close()

	s := newTestSkill(srv.Client())
	input, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, err := s.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "truncated") {
		t.Error("large content should be truncated")
	}
}
