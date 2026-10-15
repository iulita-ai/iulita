package browseroauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func TestTimeSampleNonceTLSAndNoCache(t *testing.T) {
	f := setup(t)
	nonce, err := oauthstore.Secret()
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("/oauth2/time?nonce="+nonce, nil, nil)
	var reply struct {
		Nonce, Issuer string
		ClockID       string `json:"clock_id"`
		Received      int64  `json:"received_at_unix_ms"`
		Sent          int64  `json:"sent_at_unix_ms"`
		Error         int    `json:"clock_error_ms"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || reply.Nonce != nonce || reply.Issuer != testOrigin || reply.ClockID != f.s.config.Clock.ID() || reply.Received > reply.Sent || reply.Sent > time.Now().UnixMilli() || reply.Error != 50 {
		t.Fatal("invalid clock sample", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("clock response is cacheable")
	}
	for _, query := range []string{"", "?nonce=short", "?nonce=" + nonce + "&nonce=" + nonce, "?nonce=" + nonce + "&extra=x"} {
		if bad := f.request("/oauth2/time"+query, nil, nil); bad.Code != 400 {
			t.Fatal("bad nonce accepted", bad.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://mobile.example.test/oauth2/time?nonce="+nonce, http.NoBody)
	r.Header.Set("X-Forwarded-Proto", "https")
	denied := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(denied, r)
	if denied.Code != 400 {
		t.Fatal("forwarded TLS was trusted")
	}
}
