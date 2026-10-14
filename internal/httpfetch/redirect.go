package httpfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/iulita-ai/iulita/internal/web"
)

// redirectHop records one encountered 3xx response.
type redirectHop struct {
	status   int
	location string
	url      string // resolved absolute target
}

// doWithRedirects executes the request with fully manual redirect control
// (plan §6): every hop is re-validated (SSRF, port, downgrade, origin), the
// hop budget is enforced, and mutating methods never silently downgrade to
// GET on 301/302/303 — the redirect response is returned for the caller to
// decide. 307/308 replay the buffered body within the same operation.
func (e *Engine) doWithRedirects(ctx context.Context, step Request, state *runState, scope Scope, method string, target *url.URL, origin Origin) (*http.Response, []redirectHop, error) {
	var hops []redirectHop
	visited := map[string]bool{}
	current := target
	curOrigin := origin
	methodNow := method

	for {
		if !e.policy.Enabled {
			return nil, hops, fmt.Errorf("HTTP tool disabled mid-run")
		}
		key := methodNow + "\n" + current.String()
		if visited[key] {
			// Redirect loop: stop following, surface the redirect itself.
			return nil, hops, fmt.Errorf("redirect loop detected at %s", sanitizeURLString(current))
		}
		visited[key] = true

		req, _, err := e.buildRequest(ctx, step, state, scope, methodNow, current.String())
		if err != nil {
			return nil, hops, err
		}
		resp, err := e.client.Do(req)
		if err != nil {
			return nil, hops, err
		}

		if !isRedirectStatus(resp.StatusCode) {
			return resp, hops, nil
		}

		loc := firstHeaderValue(resp.Header, "Location")
		hops = append(hops, redirectHop{status: resp.StatusCode, location: loc, url: ""})

		if step.Redirects != RedirectSameOrigin {
			// Default: return the 3xx and its Location for the caller.
			return resp, hops, nil
		}
		if len(hops) > e.limits.MaxRedirects {
			return resp, hops, nil
		}
		if loc == "" {
			return resp, hops, nil
		}

		locURL, lerr := url.Parse(loc)
		if lerr != nil {
			return resp, hops, nil //nolint:nilerr // malformed Location: surface the redirect itself
		}
		next := current.ResolveReference(locURL)
		hops[len(hops)-1].url = next.String()

		// HTTPS→HTTP downgrade is forbidden even within the same host.
		if curOrigin.Scheme == "https" && next.Scheme == "http" {
			return resp, hops, nil
		}
		// Every hop passes the full target validation again.
		if err := web.ValidateTarget(ctx, next.String(), e.resolver); err != nil {
			_ = resp.Body.Close() //nolint:errcheck // rejected hop
			return nil, hops, fmt.Errorf("redirect target rejected: %w", err)
		}
		nextOrigin := ParseOrigin(next)
		if !curOrigin.Equal(nextOrigin) {
			// Cross-origin (including subdomains and port changes) is never
			// followed automatically; the redirect is returned as-is.
			return resp, hops, nil
		}

		switch resp.StatusCode {
		case 307, 308:
			// Method and body replay (body is buffered and replayable).
			_ = resp.Body.Close() //nolint:errcheck // hop consumed
		case 301, 302, 303:
			if !isSafeMethod(methodNow) {
				// Do not silently convert a mutation into GET: return the
				// redirect response with the Location for an explicit choice.
				return resp, hops, nil
			}
			_ = resp.Body.Close() //nolint:errcheck // hop consumed
			methodNow = http.MethodGet
		default:
			return resp, hops, nil
		}
		current = next
	}
}

func isRedirectStatus(code int) bool {
	return code == 301 || code == 302 || code == 303 || code == 307 || code == 308
}

func firstHeaderValue(h http.Header, name string) string {
	values := lookupHeader(h, name)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[len(values)-1])
}
