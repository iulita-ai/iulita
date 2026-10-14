package httpfetch

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// storedCookie is one cookie with its exact-set-origin provenance.
type storedCookie struct {
	name    string
	value   string
	path    string
	expires time.Time
}

// cookieJar is the per-Run chain cookie store. The standard jar's domain
// semantics (subdomain sharing, port ignorance) are intentionally NOT used:
// every cookie is bound to the exact origin that set it, and it is only ever
// sent back to that exact origin (plan §5). Jar lifetime is one Run; nothing
// persists between tool calls in v1.
type cookieJar struct {
	mu       sync.Mutex
	byOrigin map[string][]storedCookie
}

func newCookieJar() *cookieJar {
	return &cookieJar{byOrigin: make(map[string][]storedCookie)}
}

// recordResponse stores Set-Cookie values with their origin.
func (j *cookieJar) recordResponse(origin Origin, resp *http.Response) {
	setCookies := resp.Cookies()
	if len(setCookies) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	key := origin.String()
	list := j.byOrigin[key]
	for _, c := range setCookies {
		if !validCookieName(c.Name) || !validCookieValue(c.Value) {
			continue
		}
		replaced := false
		for i := range list {
			if list[i].name == c.Name && list[i].path == c.Path {
				list[i] = storedCookie{name: c.Name, value: c.Value, path: c.Path, expires: cookieExpiry(c)}
				replaced = true
				break
			}
		}
		if !replaced {
			list = append(list, storedCookie{name: c.Name, value: c.Value, path: c.Path, expires: cookieExpiry(c)})
		}
	}
	j.byOrigin[key] = list
}

// recordRequestSent is a lifecycle hook reserved for future audit metrics.
func (j *cookieJar) recordRequestSent(Origin) {}

// cookiesFor returns cookies eligible for the exact origin and URL path.
func (j *cookieJar) cookiesFor(origin Origin, u *url.URL) []storedCookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	list := j.byOrigin[origin.String()]
	now := time.Now()
	var out []storedCookie
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	for _, c := range list {
		if !c.expires.IsZero() && c.expires.Before(now) {
			continue
		}
		if !pathMatches(c.path, path) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].name < out[k].name })
	return out
}

func pathMatches(cookiePath, reqPath string) bool {
	if cookiePath == "" {
		cookiePath = "/"
	}
	if cookiePath == reqPath {
		return true
	}
	if strings.HasPrefix(reqPath, cookiePath) {
		return strings.HasSuffix(cookiePath, "/") || strings.HasPrefix(reqPath[len(cookiePath):], "/")
	}
	return false
}

func cookieExpiry(c *http.Cookie) time.Time {
	if c.MaxAge > 0 {
		return time.Now().Add(time.Duration(c.MaxAge) * time.Second)
	}
	if !c.Expires.IsZero() {
		return c.Expires
	}
	if c.MaxAge < 0 {
		return time.Unix(0, 0) // explicitly expired
	}
	return time.Time{}
}

func validCookieName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !ok {
			return false
		}
	}
	return true
}

func validCookieValue(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x21 || c == 0x7f || c == ',' || c == ';' || c == '\\' {
			return false
		}
	}
	return true
}
