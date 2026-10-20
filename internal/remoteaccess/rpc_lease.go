package remoteaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const workerLeaseTTL = 5 * time.Second

type workerLease struct{ expires time.Time }
type workerRenewal struct {
	ID       string    `json:"id"`
	IssuedAt time.Time `json:"issued_at"`
	Kind     string    `json:"kind"`
}

func (t *RPCTransport) renewLease(ctx context.Context, id string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			data, err := json.Marshal(workerRenewal{ID: id, IssuedAt: time.Now().UTC(), Kind: "renew"})
			if err != nil {
				return
			}
			callctx, cancel := context.WithTimeout(ctx, time.Second)
			r, requestErr := http.NewRequestWithContext(callctx, http.MethodPost, strings.TrimSuffix(t.endpoint, "/execute")+"/renew", bytes.NewReader(data))
			if requestErr != nil {
				cancel()
				return
			}
			r.Header.Set("X-Iulita-Signature", signature(t.key, data))
			r.Header.Set("Content-Type", "application/json")
			response, sendErr := t.client.Do(r)
			if sendErr != nil {
				cancel()
				continue
			}
			if closeErr := response.Body.Close(); closeErr != nil {
				cancel()
				continue
			}
			cancel()
		}
	}
}
func (h *WorkerHandler) renew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2049))
	if err != nil || len(body) > 2048 || !validSignature(h.key, body, r.Header.Get("X-Iulita-Signature")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var renewal workerRenewal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&renewal) != nil || renewal.Kind != "renew" || renewal.ID == "" || time.Since(renewal.IssuedAt) > workerLeaseTTL || time.Until(renewal.IssuedAt) > time.Second {
		http.Error(w, "expired", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	lease, ok := h.leases[renewal.ID]
	if !ok || !lease.expires.After(time.Now()) {
		http.Error(w, "lease not active", http.StatusConflict)
		return
	}
	// Anchor to signed time, not receipt: replay cannot extend a lease forever.
	expiry := renewal.IssuedAt.Add(workerLeaseTTL)
	if expiry.After(lease.expires) {
		lease.expires = expiry
		h.leases[renewal.ID] = lease
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *WorkerHandler) withLease(ctx context.Context, cancel context.CancelFunc, id string, issued time.Time) func() {
	h.mu.Lock()
	h.leases[id] = workerLease{expires: issued.Add(workerLeaseTTL)}
	if !issued.Add(workerLeaseTTL).After(time.Now()) {
		cancel()
	}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				h.mu.Lock()
				lease, ok := h.leases[id]
				expired := !ok || !lease.expires.After(time.Now())
				h.mu.Unlock()
				if expired {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done); h.mu.Lock(); delete(h.leases, id); h.mu.Unlock() }
}
