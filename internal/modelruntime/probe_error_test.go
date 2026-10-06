package modelruntime

import (
	"fmt"
	"testing"
)

type syntheticProviderError struct{ code string }

func (e syntheticProviderError) Error() string          { return "private test response" }
func (e syntheticProviderError) ModelErrorCode() string { return e.code }
func TestProbeErrorCodeAllowsOnlySafeProviderCategories(t *testing.T) {
	for _, code := range []string{"insufficient_balance", "authentication_failed", "model_access_denied", "credential_product_mismatch", "quota_exhausted", "raw private response"} {
		want := code
		if code == "raw private response" {
			want = "provider_check_failed"
		}
		if got := probeErrorCode(fmt.Errorf("wrapped: %w", syntheticProviderError{code})); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}
