package deepseek

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestZaiBusinessCategoriesDoNotChangeDeepSeekAndRejectUntrustedCodes(t *testing.T) {
	for _, tc := range []struct{ prefix, body string }{
		{"deepseek completion", `{"error":{"code":"1113","message":"private-provider-payload"}}`},
		{"zai completion", `{"error":{"code":"unknown-private-value","message":"private-provider-payload"}}`},
		{"zai completion", `{"error":{"code":{},"message":"private-provider-payload"}}`},
		{"zai completion", `private-provider-payload`},
	} {
		err := errorFromResponse(tc.prefix, &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(tc.body))})
		typed, ok := err.(*apiError)
		if !ok || typed.Permanent() || typed.ModelErrorCode() != "" || typed.StatusCode() != 429 {
			t.Fatalf("incorrect unknown/DS handling: %v", err)
		}
		if strings.Contains(err.Error(), "private-provider-payload") || strings.Contains(err.Error(), "unknown-private-value") || strings.Contains(err.Error(), "1113") {
			t.Fatal("untrusted provider body leaked")
		}
	}
}
