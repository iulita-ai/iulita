package weather

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCommandGuardStopsEveryBackendBeforeHTTP(t *testing.T) {
	for _, backend := range []string{"openmeteo", "wttr", "owm"} {
		t.Run(backend, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: cardTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected HTTP") })}
			var b WeatherBackend
			switch backend {
			case "openmeteo":
				b = NewOpenMeteoBackend(client)
			case "wttr":
				b = NewWttrInBackend(client)
			case "owm":
				b = NewOWMBackend(client, "fixture-key")
			}
			ctx := WithCommandGuard(context.Background(), func(context.Context) error { return context.Canceled })
			if _, err := b.Fetch(ctx, "Paris", 4); err == nil || calls != 0 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestCommandGuardCoversRedirectWithoutMutatingSharedClient(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/first" {
			allowed.Store(false)
			http.Redirect(w, r, "/second", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := server.Client()
	originalTransport := client.Transport
	ctx := WithCommandGuard(context.Background(), func(context.Context) error {
		if !allowed.Load() {
			return context.Canceled
		}
		return nil
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/first", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := doWeatherRequest(client, req)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("redirect escaped guard: calls=%d err=%v", calls.Load(), err)
	}
	if client.Transport != originalTransport || client.CheckRedirect != nil {
		t.Fatal("shared client mutated")
	}
}

func TestCommandGuardStopsAfterGeocodingWithoutFallbackDisclosure(t *testing.T) {
	allowed, calls := true, 0
	s := New(nil, nil, nil)
	s.httpClient = &http.Client{Transport: cardTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.Contains(r.URL.Host, "geocoding") {
			t.Error("request continued after route became unavailable")
		}
		allowed = false // deletion/pause/disable while the first request was in flight
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"results":[{"name":"Paris","country":"France","latitude":48.85,"longitude":2.35,"timezone":"Europe/Paris"}]}`)), Header: http.Header{}}, nil
	})}
	ctx := WithCommandGuard(context.Background(), func(context.Context) error {
		if !allowed {
			return context.Canceled
		}
		return nil
	})
	if _, err := s.ForecastCard(ctx, "Paris", 4); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
