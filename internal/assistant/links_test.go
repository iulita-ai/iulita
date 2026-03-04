package assistant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractURLs(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		max      int
		expected int
	}{
		{"no urls", "just some text", 5, 0},
		{"one url", "check https://example.com please", 5, 1},
		{"multiple urls", "see https://a.com and https://b.com and https://c.com", 5, 3},
		{"max limit", "see https://a.com and https://b.com and https://c.com", 2, 2},
		{"http scheme", "visit http://example.com now", 5, 1},
		{"url in brackets", "see [https://example.com] here", 5, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			urls := extractURLs(tt.text, tt.max)
			if len(urls) != tt.expected {
				t.Errorf("expected %d urls, got %d: %v", tt.expected, len(urls), urls)
			}
		})
	}
}

func TestExtractURLs_ValidURLs(t *testing.T) {
	urls := extractURLs("visit https://example.com/path?q=1#frag here", 5)
	if len(urls) != 1 {
		t.Fatalf("expected 1 url, got %d", len(urls))
	}
	if !strings.HasPrefix(urls[0], "https://example.com/path") {
		t.Errorf("unexpected url: %s", urls[0])
	}
}

const testArticleHTML = `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<article>
<h1>Test Page</h1>
<p>This is the main content of a test article. It needs to be long enough for
the readability parser to recognize it as meaningful content. Here are several
more sentences to pad out the content and ensure proper extraction by the
readability algorithm.</p>
<p>A second paragraph provides additional signal to the parser that this is
indeed the main article content and not sidebar or navigation text. The more
paragraphs we have, the more confident the parser becomes.</p>
</article>
</body>
</html>`

func TestEnrichWithLinks_NoURLs(t *testing.T) {
	result := enrichWithLinks("hello world", 3)
	if result != "hello world" {
		t.Errorf("expected unchanged text, got: %s", result)
	}
}

func TestEnrichWithLinks_WithArticle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(testArticleHTML))
	}))
	defer srv.Close()

	text := "check this " + srv.URL
	result := enrichWithLinks(text, 3)

	if !strings.Contains(result, "<<<EXTERNAL_CONTENT") {
		t.Error("result should contain EXTERNAL_CONTENT marker")
	}
	if !strings.Contains(result, "EXTERNAL and UNTRUSTED") {
		t.Error("result should contain untrusted warning")
	}
	if !strings.Contains(result, "main content") {
		t.Error("result should contain article text")
	}
}

func TestEnrichWithLinks_FailedFetch(t *testing.T) {
	// Use a server that always 500s.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	text := "check this " + srv.URL
	result := enrichWithLinks(text, 3)

	// Should return original text since fetch failed.
	if result != text {
		t.Errorf("expected original text on failed fetch, got: %s", result)
	}
}

func TestEnrichWithLinks_ExcerptTruncation(t *testing.T) {
	bigParagraph := strings.Repeat("Long text for truncation testing. ", 200)
	html := `<!DOCTYPE html><html><head><title>Big</title></head><body><article><h1>Big</h1><p>` +
		bigParagraph + `</p></article></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	}))
	defer srv.Close()

	text := "see " + srv.URL
	result := enrichWithLinks(text, 3)

	if !strings.Contains(result, "...") {
		t.Error("long excerpt should be truncated with ...")
	}
}
