package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOfficialEndpointRejectsCredentialAndHostConfusion(t *testing.T) {
	for _, endpoint := range []string{"http://api.deepseek.com/v1", "https://api.deepseek.com.evil.test/v1", "https://key@api.deepseek.com/v1", "https://api.deepseek.com:443/v1", "https://api.deepseek.com/v1?secret=value", "https://api.deepseek.com/v1#fragment", "https://api.z.ai/api/coding/paas/v4", "https://127.0.0.1/v1", "https://api.deepseek.com/v1/../v1"} {
		if _, err := OfficialModelEndpoint("deepseek", endpoint); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	if got, err := OfficialModelEndpoint("deepseek", "https://api.deepseek.com/"); err != nil || got != "https://api.deepseek.com/v1" {
		t.Fatalf("canonical endpoint %s %v", got, err)
	}
}
func TestOfficialClientPreservesTransportAndNeverFollowsRedirect(t *testing.T) {
	calls := 0
	base := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.deepseek.com" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Fatal("wrong identity")
		}
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://evil.test/capture"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	client, endpoint, err := NewOfficialModelClient("deepseek", "", base)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer synthetic")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 307 || calls != 1 {
		t.Fatal("redirect was followed")
	}
	bad, _ := http.NewRequest(http.MethodPost, "https://evil.test/chat/completions", nil)
	if _, err := client.Do(bad); err == nil || calls != 1 {
		t.Fatal("credentials could escape binding")
	}
	req, _ = http.NewRequest(http.MethodPost, endpoint+"/models", nil)
	if _, err := client.Do(req); err == nil || calls != 1 {
		t.Fatal("unexpected endpoint method reached transport")
	}
}
