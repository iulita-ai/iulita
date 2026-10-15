package browseroauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

// timeSample is TLS-authenticated and nonce-bound, and never accepts forwarded
// timestamps or client clock readings. The client must include its measured RTT,
// resolution, drift and sample age when calculating a conservative deadline.
func (s *Server) timeSample(w http.ResponseWriter, r *http.Request) {
	id, received := s.config.Clock.Read()
	query, err := url.ParseQuery(r.URL.RawQuery)
	if r.Method != http.MethodGet || err != nil || len(r.URL.RawQuery) > 64 || len(query) != 1 || !one(query, []string{"nonce"}) || !oauthstore.ValidSecret(query.Get("nonce")) || (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		s.fail(w, http.StatusBadRequest, "Неверный запрос времени.")
		return
	}
	if !s.limit(w, r, "time.ip", ip(r), 300) {
		return
	}
	currentID, sent := s.config.Clock.Read()
	if id != currentID || sent.Before(received) || time.Since(received) > 2*time.Second {
		s.fail(w, http.StatusServiceUnavailable, "Время сервера изменилось; требуется новый запрос.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Issuer     string `json:"issuer"`
		Nonce      string `json:"nonce"`
		ClockID    string `json:"clock_id"`
		Received   int64  `json:"received_at_unix_ms"`
		Sent       int64  `json:"sent_at_unix_ms"`
		Resolution int    `json:"resolution_ms"`
		ClockError int    `json:"clock_error_ms"`
		MaxAge     int    `json:"max_sample_age_ms"`
	}{s.origin, query.Get("nonce"), id, received.UnixMilli(), sent.UnixMilli(), 1, 50, 30000}); err != nil {
		return // The response may already be partially written; do not append an error body.
	}
}
