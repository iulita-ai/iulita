package browseroauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserFormsPreserveSameOriginPost(t *testing.T) {
	f := setup(t)
	w := f.request(authURL(f.a), nil, nil)
	if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("form policy %q makes browser POST Origin null; want same-origin", got)
	}
	v, cookie := f.login(t, f.a)
	// The consent page must retain browser Origin as well as the password page.
	w = f.request(authURL(f.a), nil, nil)
	first := fields(t, w)
	first.Set("username", "owner")
	first.Set("password", "correct horse fixture")
	w = f.request("/oauth2/login", first, w.Result().Cookies()[0])
	if w.Code != http.StatusOK || w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("consent form does not preserve same-origin POST")
	}
	// The OAuth redirect and non-form responses must not disclose their URL.
	w = f.request("/oauth2/consent", v, cookie)
	if w.Code != http.StatusSeeOther || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("OAuth callback redirect must retain no-referrer")
	}
}

func TestBrowserFormStillRejectsNullMissingAndForeignOrigin(t *testing.T) {
	f := setup(t)
	for _, origin := range []string{"", "null", "https://foreign.example.test", testOrigin + ".attacker.invalid"} {
		t.Run(origin, func(t *testing.T) {
			for _, path := range []string{"/oauth2/login", "/oauth2/consent"} {
				r := httptest.NewRequest(http.MethodPost, testOrigin+path, strings.NewReader("flow_id=fixture&csrf=fixture"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if origin != "" {
					r.Header.Set("Origin", origin)
				}
				w := httptest.NewRecorder()
				f.s.Handler().ServeHTTP(w, r)
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Неверный запрос формы") {
					t.Fatal("unsafe form origin was accepted")
				}
			}
		})
	}
}
