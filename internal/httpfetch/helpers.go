package httpfetch

import (
	"net/url"
	"time"
)

// parseURL is a small wrapper used where errors are already contextualized.
func parseURL(raw string) (*url.URL, error) { return url.Parse(raw) }

// urlQueryEscape keeps the form-encoding convention for query components.
func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// timeNow is the injectable clock (tests override via engine construction).
var timeNow = time.Now
