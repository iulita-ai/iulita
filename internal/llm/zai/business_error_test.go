package zai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
)

func TestAccountRejectionsDoNotRetryOrFallbackAndRedactProviderBody(t *testing.T) {
	for _, tc := range []struct {
		code, category string
		status         int
	}{
		{"1113", "insufficient_balance", 429},
		{"1315", "credential_product_mismatch", 429},
		{"1311", "model_access_denied", 429},
		{"1308", "quota_exhausted", 429},
		{"1317", "quota_exhausted", 429},
		{"1309", "quota_exhausted", 429},
		{"1000", "authentication_failed", 401},
		{"1220", "model_access_denied", 403},
	} {
		for _, numeric := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/numeric=%t", tc.code, numeric), func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(tc.status)
					code := fmt.Sprintf("%q", tc.code)
					if numeric {
						code = tc.code
					}
					fmt.Fprintf(w, `{"error":{"code":%s,"message":"private-provider-payload"}}`, code)
				}))
				defer srv.Close()
				raw := New("key", "glm-5.3-flash", 128, srv.URL, srv.Client(), nil)
				retry := llm.NewRetryProvider(wrappedProvider{raw}, llm.RetryConfig{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
				fallback := &neverFallback{t: t}
				_, err := llm.NewFallbackProvider(retry, fallback).Complete(context.Background(), llm.Request{Message: "check"})
				var typed interface {
					ModelErrorCode() string
					Permanent() bool
					StatusCode() int
				}
				if !errors.As(err, &typed) || typed.ModelErrorCode() != tc.category || !typed.Permanent() || typed.StatusCode() != tc.status {
					t.Fatalf("incorrect category: %v", err)
				}
				if calls.Load() != 1 {
					t.Fatalf("permanent rejection retried %d times", calls.Load())
				}
				if strings.Contains(err.Error(), "private-provider-payload") || strings.Contains(err.Error(), tc.code) {
					t.Fatal("upstream message or raw code leaked")
				}
				// Streaming uses the same safe classification.
				_, err = raw.CompleteStream(context.Background(), llm.Request{Message: "check"}, func(string) { t.Fatal("rejected stream emitted text") })
				if !errors.As(err, &typed) || typed.ModelErrorCode() != tc.category {
					t.Fatalf("stream category lost: %v", err)
				}
			})
		}
	}
}

type neverFallback struct{ t *testing.T }

type wrappedProvider struct{ inner llm.Provider }

func (p wrappedProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	response, err := p.inner.Complete(ctx, req)
	if err != nil {
		err = fmt.Errorf("wrapped adapter: %w", err)
	}
	return response, err
}

func (p *neverFallback) Complete(context.Context, llm.Request) (llm.Response, error) {
	p.t.Fatal("account rejection switched provider")
	return llm.Response{}, nil
}

func TestTransientAndUnknown429StillRetry(t *testing.T) {
	for _, code := range []string{"1302", "1305", "unknown"} {
		t.Run(code, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(429)
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"private-provider-payload"}}`, code)
			}))
			defer srv.Close()
			raw := New("key", "glm-5.3-flash", 128, srv.URL, srv.Client(), nil)
			_, err := llm.NewRetryProvider(raw, llm.RetryConfig{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}).Complete(context.Background(), llm.Request{Message: "check"})
			if err == nil || calls.Load() != 3 || strings.Contains(err.Error(), "private-provider-payload") {
				t.Fatalf("bad transient handling: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}
