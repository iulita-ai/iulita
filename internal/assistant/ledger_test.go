package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
)

func ledgerResult(content string) llm.ToolResult {
	return llm.ToolResult{ToolCallID: "tc1", Content: content, IsError: false}
}

func TestLedgerDedupUnchangedForNormalTools(t *testing.T) {
	l := NewExplorationLedger()
	tc := llm.ToolCall{ID: "1", Name: "websearch", Input: json.RawMessage(`{"q":"go http"}`)}
	l.Record(tc, ledgerResult("result-1"))

	cached, isDup := l.IsDuplicate(tc)
	if !isDup || cached != "result-1" {
		t.Fatalf("dedup broken for normal tools: %v %q", isDup, cached)
	}
}

func TestLedgerNonCacheableExcludedFromDedup(t *testing.T) {
	l := NewExplorationLedger()
	tc := llm.ToolCall{ID: "1", Name: "http_request", Input: json.RawMessage(`{"version":1,"request":{"method":"POST","url":"https://api.example.test/v1/items"}}`)}

	// A policy-carrying skill records the same input as a separate operation.
	l.RecordNonCacheable(tc, ledgerResult(`{"outcome":"completed"}`), "POST create")

	// Identical re-issue must NOT be served from any cache.
	if _, isDup := l.IsDuplicate(tc); isDup {
		t.Fatal("non-cacheable call was deduplicated")
	}

	l.RecordNonCacheable(tc, ledgerResult(`{"outcome":"completed"}`), "POST create")
	if _, isDup := l.IsDuplicate(tc); isDup {
		t.Fatal("second non-cacheable call was deduplicated")
	}
}

func TestLedgerSummaryUsesSafeDisplay(t *testing.T) {
	l := NewExplorationLedger()
	secret := `{"version":1,"request":{"auth":{"type":"basic","username":"u","password":"topsecret"},"url":"https://api.example.test/v1/me"}}`
	l.RecordNonCacheable(
		llm.ToolCall{ID: "1", Name: "http_request", Input: json.RawMessage(secret)},
		ledgerResult("{}"),
		"GET me",
	)
	summary := l.Summary()
	if strings.Contains(summary, "topsecret") {
		t.Fatalf("summary leaks input secrets: %s", summary)
	}
	if !strings.Contains(summary, "GET me") {
		t.Fatalf("summary missing display line: %s", summary)
	}
}
