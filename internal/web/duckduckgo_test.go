package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testDDGHTML = `<!DOCTYPE html>
<html>
<body>
<div class="results">
  <div class="result">
    <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1&amp;rut=abc">First Result</a>
    <a class="result__snippet">This is the first snippet with useful info.</a>
  </div>
  <div class="result">
    <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage2&amp;rut=def">Second Result</a>
    <a class="result__snippet">Second snippet here.</a>
  </div>
  <div class="result">
    <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage3&amp;rut=ghi">Third Result</a>
    <a class="result__snippet">Third snippet.</a>
  </div>
</div>
</body>
</html>`

func TestDDGClient_ParseResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(testDDGHTML))
	}))
	defer srv.Close()

	// Override the URL by using a custom client that redirects to our test server.
	// Since DDGClient hardcodes the URL, we test parseDDGHTML directly.
	results := parseDDGHTML(testDDGHTML, 5)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	if results[0].Title != "First Result" {
		t.Errorf("result[0].Title = %q, want %q", results[0].Title, "First Result")
	}
	if results[0].URL != "https://example.com/page1" {
		t.Errorf("result[0].URL = %q, want %q", results[0].URL, "https://example.com/page1")
	}
	if results[0].Description != "This is the first snippet with useful info." {
		t.Errorf("result[0].Description = %q", results[0].Description)
	}

	if results[1].URL != "https://example.com/page2" {
		t.Errorf("result[1].URL = %q", results[1].URL)
	}
}

func TestDDGClient_MaxResults(t *testing.T) {
	results := parseDDGHTML(testDDGHTML, 1)
	if len(results) != 1 {
		t.Errorf("expected 1 result with max=1, got %d", len(results))
	}
}

func TestDDGClient_EmptyHTML(t *testing.T) {
	results := parseDDGHTML("<html><body></body></html>", 5)
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty HTML, got %d", len(results))
	}
}

func TestExtractDDGURL(t *testing.T) {
	tests := []struct {
		href string
		want string
	}{
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=abc", "https://example.com"},
		{"https://direct.example.com", "https://direct.example.com"},
		{"http://direct.example.com", "http://direct.example.com"},
		{"/relative/path", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractDDGURL(tt.href)
		if got != tt.want {
			t.Errorf("extractDDGURL(%q) = %q, want %q", tt.href, got, tt.want)
		}
	}
}
