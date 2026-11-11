package weather

import (
	"context"
	"net/http"
)

type commandGuardKey struct{}

// WithCommandGuard lets a direct command revalidate its private route before
// every backend request, including geocoding and fallback after a failed fetch.
func WithCommandGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, commandGuardKey{}, guard)
}

// CheckCommandGuard also protects multi-page delivery; absent a command guard,
// only cancellation is checked (e.g. unrelated personal status replies).
func CheckCommandGuard(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard, ok := ctx.Value(commandGuardKey{}).(func(context.Context) error); ok {
		return guard(ctx)
	}
	return nil
}

type commandGuardTransport struct{ base http.RoundTripper }

// RoundTrip revalidates authority for every request, including redirects.
func (t commandGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := CheckCommandGuard(req.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func doWeatherRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	if err := CheckCommandGuard(req.Context()); err != nil {
		return nil, err
	}
	if _, guarded := req.Context().Value(commandGuardKey{}).(func(context.Context) error); !guarded {
		return client.Do(req)
	}
	// Copy only the client configuration; preserve its transport/pool, timeout,
	// cookie jar and redirect policy. Every redirect still gets a fresh guard.
	guarded := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	guarded.Transport = commandGuardTransport{base: base}
	return guarded.Do(req)
}
