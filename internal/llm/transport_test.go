package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
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

func TestPinnedModelEndpointDefaultsAndRejectsHiddenRecipients(t *testing.T) {
	for provider, expected := range map[string]string{"claude": "https://api.anthropic.com", "openai": "https://api.openai.com/v1", "deepseek": "https://api.deepseek.com/v1", "zai": "https://api.z.ai/api/paas/v4"} {
		if actual, err := PinnedModelEndpoint(provider, ""); err != nil || actual != expected {
			t.Fatalf("%s endpoint = %q, %v", provider, actual, err)
		}
	}
	for _, endpoint := range []string{"", "/relative", "https://key@model.test/v1", "https://model.test/v1?key=secret", "https://model.test/v1?", "https://model.test/v1#secret", "https://model.test/%76%31", "file:///tmp/model"} {
		if _, err := PinnedModelEndpoint("ollama", endpoint); err == nil {
			t.Fatalf("accepted hidden or missing recipient %q", endpoint)
		}
	}
	if actual, err := PinnedModelEndpoint("ollama", "http://127.0.0.1:11434/custom"); err != nil || actual != "http://127.0.0.1:11434/custom" {
		t.Fatalf("explicit local recipient = %q, %v", actual, err)
	}
}

func TestPinnedModelClientPreservesProxyTransportAndRejectsOriginChanges(t *testing.T) {
	calls := 0
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	base := &http.Client{Jar: jar, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://other.test/capture"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	client, endpoint, err := NewPinnedModelClient("claude", "https://model.test/custom", base)
	if err != nil {
		t.Fatal(err)
	}
	if base.Jar != jar || base.CheckRedirect != nil || client.Jar != nil {
		t.Fatal("caller client mutated or UI cookie jar inherited")
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/v1/messages", strings.NewReader("private"))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || calls != 1 {
		t.Fatal("configured transport bypassed or redirect followed")
	}
	for _, target := range []string{"https://other.test/v1/messages", "http://model.test/v1/messages", "https://model.test:443/v1/messages", "https://key@model.test/v1/messages"} {
		forbidden, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader("private"))
		if _, sendErr := client.Do(forbidden); sendErr == nil || calls != 1 {
			t.Fatalf("unexpected recipient reached transport: %s", target)
		}
	}
	request, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/v1/messages", strings.NewReader("private"))
	request.Host = "other.test"
	if _, sendErr := client.Do(request); sendErr == nil || calls != 1 {
		t.Fatal("Host override reached transport")
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
	bad, _ := http.NewRequest(http.MethodPost, "https://evil.test/chat/completions", http.NoBody)
	if _, err := client.Do(bad); err == nil || calls != 1 {
		t.Fatal("credentials could escape binding")
	}
	req, _ = http.NewRequest(http.MethodPost, endpoint+"/models", http.NoBody)
	if _, err := client.Do(req); err == nil || calls != 1 {
		t.Fatal("unexpected endpoint method reached transport")
	}
}
