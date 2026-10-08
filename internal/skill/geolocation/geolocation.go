package geolocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/llm"
)

// SynthesisRouteHint implements skill.SynthesisModelDeclarer.
func (s *Skill) SynthesisRouteHint() string { return llm.RouteHintCheap }

const (
	// IP detection endpoints.
	ipifyURL   = "https://api.ipify.org?format=json"
	icanhazURL = "https://icanhazip.com"

	// Geolocation endpoints.
	ipAPIURL  = "http://ip-api.com/json/" // free tier requires HTTP
	ipapiURL  = "https://ipapi.co/"       // fallback
	ipinfoURL = "https://ipinfo.io/"      // paid/free-tier with token

	// Forward geocoding (place name → coordinates) via the public Nominatim
	// instance. Compiled-in on purpose (D20): the only operator escape hatch is
	// the skills.geolocation.geocode_enabled kill-switch.
	nominatimSearchURL = "https://nominatim.openstreetmap.org/search"
	nominatimUA        = "iulita-bot/1.0 (https://iulita.ai)" // descriptive UA with contact, per Nominatim policy
	geocodeTimeout     = 5 * time.Second
	geocodeMinInterval = time.Second // public instance policy: max 1 req/s

	maxResponseSize = 64 * 1024 // 64 KB
	userAgent       = "iulita-bot/1.0"
)

// geocodeMu + lastGeocodeAt throttle Nominatim calls to ≥1s apart (package
// level: one budget across all Skill instances and goroutines).
var (
	geocodeMu     sync.Mutex
	lastGeocodeAt time.Time
)

// Skill provides IP geolocation lookups.
type Skill struct {
	httpClient     *http.Client
	mu             sync.RWMutex
	apiKey         string // ipinfo.io token (optional)
	cfgStore       configReader
	bus            *eventbus.Bus // nil-safe; observability
	logger         *zap.Logger   // nil-safe; geocode diagnostics (plan §13)
	geocodeEnabled bool          // skills.geolocation.geocode_enabled (default true)
}

// configReader re-reads config effective values on hot-reload (config.Store
// satisfies it). Declared here to avoid an import cycle.
type configReader interface {
	GetEffective(key string) (string, bool)
}

// New creates a new geolocation skill.
func New(httpClient *http.Client) *Skill {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Skill{httpClient: httpClient, logger: zap.NewNop(), geocodeEnabled: true}
}

// SetLogger wires the diagnostic logger (geocode ok/fail rows, plan §13).
// Never logs the request URL — it embeds the user query.
func (s *Skill) SetLogger(logger *zap.Logger) {
	if logger == nil {
		return
	}
	s.mu.Lock()
	s.logger = logger
	s.mu.Unlock()
}

// stripURLError unwraps *url.Error, whose string embeds the full request URL —
// including the user's place query — before the error is logged (plan §13:
// never log the request URL).
func stripURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

func (s *Skill) log() *zap.Logger {
	s.mu.RLock()
	l := s.logger
	s.mu.RUnlock()
	if l == nil {
		return zap.NewNop()
	}
	return l
}

// SetReloader wires the config store for hot-reload re-reads and seeds the
// geocode kill-switch from the effective value (base config.toml + DB override
// — without this, a base-config value would be silently inert until the first
// runtime change). Geolocation has no capability gate (its IP path needs no
// credentials), so unlike todoist there is no capabilityAdder parameter.
func (s *Skill) SetReloader(cfgStore configReader) {
	s.cfgStore = cfgStore
	if cfgStore == nil {
		return
	}
	if v, ok := cfgStore.GetEffective("skills.geolocation.geocode_enabled"); ok {
		s.mu.Lock()
		s.geocodeEnabled = !isGeocodeDisabledValue(v)
		s.mu.Unlock()
	}
}

// isGeocodeDisabledValue normalizes the kill-switch value so free-text input
// like "False", "no" or "off" disables geocoding instead of being ignored.
func isGeocodeDisabledValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no", "off":
		return true
	}
	return false
}

// SetBus wires the observability event bus (deferred wiring).
func (s *Skill) SetBus(bus *eventbus.Bus) { s.bus = bus }

// Name is the tool name exposed to the LLM.
func (s *Skill) Name() string { return "geolocation" }

func (s *Skill) Description() string {
	return "Determine the user's public IP address and geographic location (country, city, timezone, ISP). " +
		"Can also look up location for a specific IP address, and resolve a place name to coordinates " +
		"(action=geocode) for sharing it on the map."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"ip": {
			"type": "string",
			"description": "IP address to look up. If omitted, auto-detects the user's public IP."
		},
		"action": {
			"type": "string",
			"enum": ["geocode"],
			"description": "Set to \"geocode\" to look up coordinates for a place name instead of an IP."
		},
		"query": {
			"type": "string",
			"description": "Place name or address to geocode (required when action=geocode)."
		}
	}
}`)
}

// OnConfigChanged implements skill.ConfigReloadable.
func (s *Skill) OnConfigChanged(key, value string) {
	switch key {
	case "skills.geolocation.api_key":
		s.mu.Lock()
		s.apiKey = value
		s.mu.Unlock()
	case "skills.geolocation.geocode_enabled":
		// Re-read the effective value (the `value` param is empty on deletions;
		// deletion means enabled — registry quirk).
		enabled := true
		if s.cfgStore != nil {
			if v, ok := s.cfgStore.GetEffective("skills.geolocation.geocode_enabled"); ok {
				enabled = !isGeocodeDisabledValue(v)
			}
		} else {
			enabled = !isGeocodeDisabledValue(value)
		}
		s.mu.Lock()
		s.geocodeEnabled = enabled
		s.mu.Unlock()
	}
}

type geoInput struct {
	IP     string `json:"ip"`
	Action string `json:"action"` // "" (ip lookup, default) | "geocode"
	Query  string `json:"query"`
}

// geoResult holds normalized geolocation data.
type geoResult struct {
	IP          string `json:"ip"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Region      string `json:"region"`
	City        string `json:"city"`
	Timezone    string `json:"timezone"`
	ISP         string `json:"isp"`
}

// Execute runs the IP geolocation lookup or, when action=geocode, the forward
// geocode (place name → coordinates).
func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in geoInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	// Forward geocoding (place name → coordinates) is a distinct action.
	// Nonconforming enum values (wrong case, padded) must still reach the
	// geocode branch — falling through to the IP path would answer a place
	// lookup with the SERVER's location.
	switch action := strings.ToLower(strings.TrimSpace(in.Action)); action {
	case "geocode":
		return s.geocode(ctx, strings.TrimSpace(in.Query))
	case "":
		// IP lookup (default) — but a query without an action is a misformed
		// geocode call (action is optional in the schema and commonly omitted);
		// answering it from the server IP would be confidently wrong.
		if strings.TrimSpace(in.Query) != "" && strings.TrimSpace(in.IP) == "" {
			return s.geocode(ctx, strings.TrimSpace(in.Query))
		}
	default:
		return fmt.Sprintf("Unknown action %q — use action=\"geocode\" for place-name lookups.", in.Action), nil
	}

	ip := strings.TrimSpace(in.IP)

	if ip == "" {
		detectedIP, err := s.detectPublicIP(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to detect public IP: %w", err)
		}
		ip = detectedIP
	}

	// Validate and reject non-public IPs to prevent SSRF.
	if msg := validatePublicIP(ip); msg != "" {
		return msg, nil
	}

	result, err := s.geoLookup(ctx, ip)
	if err != nil {
		return fmt.Sprintf("Unable to determine location for IP %s: %s. The service may be temporarily unavailable.", ip, err), nil
	}

	return formatResult(result), nil
}

// detectPublicIP tries multiple services to determine the public IP.
func (s *Skill) detectPublicIP(ctx context.Context) (string, error) {
	// Try ipify first (JSON response).
	ip, err := s.detectViaIpify(ctx)
	if err == nil {
		return ip, nil
	}

	// Fallback to icanhazip (plain text).
	ip, err2 := s.detectViaIcanhazip(ctx)
	if err2 == nil {
		return ip, nil
	}

	return "", fmt.Errorf("all IP detection services failed: ipify: %w, icanhazip: %v", err, err2)
}

func (s *Skill) detectViaIpify(ctx context.Context) (string, error) {
	body, err := s.httpGet(ctx, ipifyURL)
	if err != nil {
		return "", err
	}

	var resp struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse ipify response: %w", err)
	}
	if resp.IP == "" {
		return "", fmt.Errorf("ipify returned empty IP")
	}
	return resp.IP, nil
}

func (s *Skill) detectViaIcanhazip(ctx context.Context) (string, error) {
	body, err := s.httpGet(ctx, icanhazURL)
	if err != nil {
		return "", err
	}

	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "", fmt.Errorf("icanhazip returned empty response")
	}
	// Validate it looks like an IP.
	if _, err := netip.ParseAddr(ip); err != nil {
		return "", fmt.Errorf("icanhazip returned invalid IP %q: %w", ip, err)
	}
	return ip, nil
}

// geoLookup tries multiple geolocation providers.
func (s *Skill) geoLookup(ctx context.Context, ip string) (*geoResult, error) {
	s.mu.RLock()
	apiKey := s.apiKey
	s.mu.RUnlock()

	// If API key is configured, try ipinfo.io first.
	if apiKey != "" {
		result, err := s.lookupIPInfo(ctx, ip, apiKey)
		if err == nil {
			return result, nil
		}
	}

	// Primary: ip-api.com (free, no auth).
	result, err := s.lookupIPAPI(ctx, ip)
	if err == nil {
		return result, nil
	}

	// Fallback: ipapi.co.
	result, err2 := s.lookupIPAPICo(ctx, ip)
	if err2 == nil {
		return result, nil
	}

	return nil, fmt.Errorf("ip-api.com: %w, ipapi.co: %v", err, err2)
}

// lookupIPAPI uses ip-api.com (free tier, HTTP only, 45 req/min).
func (s *Skill) lookupIPAPI(ctx context.Context, ip string) (*geoResult, error) {
	url := ipAPIURL + ip + "?fields=status,message,country,countryCode,regionName,city,timezone,isp,query"
	body, err := s.httpGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		RegionName  string `json:"regionName"`
		City        string `json:"city"`
		Timezone    string `json:"timezone"`
		ISP         string `json:"isp"`
		Query       string `json:"query"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse ip-api response: %w", err)
	}
	if resp.Status != "success" {
		msg := resp.Message
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("ip-api: %s", msg)
	}

	return &geoResult{
		IP:          resp.Query,
		Country:     resp.Country,
		CountryCode: resp.CountryCode,
		Region:      resp.RegionName,
		City:        resp.City,
		Timezone:    resp.Timezone,
		ISP:         resp.ISP,
	}, nil
}

// lookupIPAPICo uses ipapi.co (free tier, 1000 req/day).
func (s *Skill) lookupIPAPICo(ctx context.Context, ip string) (*geoResult, error) {
	url := ipapiURL + ip + "/json/"
	body, err := s.httpGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp struct {
		IP          string `json:"ip"`
		Country     string `json:"country_name"`
		CountryCode string `json:"country_code"`
		Region      string `json:"region"`
		City        string `json:"city"`
		Timezone    string `json:"timezone"`
		Org         string `json:"org"`
		Error       bool   `json:"error"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse ipapi.co response: %w", err)
	}
	if resp.Error {
		reason := resp.Reason
		if reason == "" {
			reason = "unknown error"
		}
		return nil, fmt.Errorf("ipapi.co: %s", reason)
	}

	return &geoResult{
		IP:          resp.IP,
		Country:     resp.Country,
		CountryCode: resp.CountryCode,
		Region:      resp.Region,
		City:        resp.City,
		Timezone:    resp.Timezone,
		ISP:         resp.Org,
	}, nil
}

// lookupIPInfo uses ipinfo.io (requires API token via Bearer header).
func (s *Skill) lookupIPInfo(ctx context.Context, ip, token string) (*geoResult, error) {
	url := ipinfoURL + ip + "/json"
	body, err := s.httpGetWithAuth(ctx, url, "Bearer "+token)
	if err != nil {
		return nil, err
	}

	var resp struct {
		IP       string `json:"ip"`
		City     string `json:"city"`
		Region   string `json:"region"`
		Country  string `json:"country"` // 2-letter code
		Timezone string `json:"timezone"`
		Org      string `json:"org"`
		Error    *struct {
			Title string `json:"title"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse ipinfo response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("ipinfo: %s", resp.Error.Title)
	}

	return &geoResult{
		IP:          resp.IP,
		Country:     "", // ipinfo.io returns only country code
		CountryCode: resp.Country,
		Region:      resp.Region,
		City:        resp.City,
		Timezone:    resp.Timezone,
		ISP:         resp.Org,
	}, nil
}

// httpGetWithAuth performs a GET request with an Authorization header.
func (s *Skill) httpGetWithAuth(ctx context.Context, url, authHeader string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authHeader)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// httpGet performs a GET request with context, User-Agent, and size limits.
func (s *Skill) httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// validatePublicIP checks that the IP is a valid, globally-routable public address.
// Returns an empty string if valid, or a user-friendly error message otherwise.
func validatePublicIP(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return fmt.Sprintf("Invalid IP address: %s", ip)
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return fmt.Sprintf("IP address %s is not a public routable address.", ip)
	}
	// Block CGN (100.64.0.0/10) — not covered by IsPrivate().
	if addr.Is4() {
		b := addr.As4()
		if b[0] == 100 && b[1] >= 64 && b[1] <= 127 {
			return fmt.Sprintf("IP address %s is not a public routable address.", ip)
		}
	}
	return ""
}

func formatResult(r *geoResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "IP: %s\n", r.IP)

	if r.Country != "" {
		fmt.Fprintf(&b, "Country: %s (%s)\n", r.Country, r.CountryCode)
	} else if r.CountryCode != "" {
		fmt.Fprintf(&b, "Country: %s\n", r.CountryCode)
	}

	if r.Region != "" {
		fmt.Fprintf(&b, "Region: %s\n", r.Region)
	}
	if r.City != "" {
		fmt.Fprintf(&b, "City: %s\n", r.City)
	}
	if r.Timezone != "" {
		fmt.Fprintf(&b, "Timezone: %s\n", r.Timezone)
	}
	if r.ISP != "" {
		fmt.Fprintf(&b, "ISP: %s\n", r.ISP)
	}

	return b.String()
}

// geocode resolves a place name to coordinates via Nominatim (forward geocode).
// Failures degrade to friendly strings, never errors (the LLM loop must not
// retry). The request URL is never logged — it embeds the user query.
func (s *Skill) geocode(ctx context.Context, query string) (string, error) {
	s.mu.RLock()
	enabled := s.geocodeEnabled
	s.mu.RUnlock()
	if !enabled {
		return "Place-name lookup (geocoding) is turned off on this server.", nil
	}
	if query == "" {
		return "No place name given to geocode.", nil
	}
	start := time.Now()

	// Keep request STARTS ≥1s apart (Nominatim policy). The critical section is
	// check+stamp only — the HTTP call runs OUTSIDE the lock, so concurrent
	// callers (e.g. parallel sub-agents) fail fast to "busy" instead of
	// queueing behind the full upstream latency.
	geocodeMu.Lock()
	if time.Since(lastGeocodeAt) < geocodeMinInterval {
		geocodeMu.Unlock()
		s.publishGeocode(ctx, "throttled")
		s.log().Debug("geocode throttled", zap.String("direction", "forward"))
		return "Geocoder is busy — try again in a moment.", nil
	}
	lastGeocodeAt = time.Now()
	geocodeMu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, geocodeTimeout)
	defer cancel()

	reqURL := nominatimSearchURL + "?q=" + url.QueryEscape(query) + "&format=jsonv2&limit=1"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		s.publishGeocode(ctx, "error")
		s.log().Warn("geocode failed", zap.String("direction", "forward"), zap.String("outcome", "error"), zap.Error(stripURLError(err)))
		return "Geocoding request failed.", nil
	}
	req.Header.Set("User-Agent", nominatimUA)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.publishGeocode(ctx, "error")
		s.log().Warn("geocode failed", zap.String("direction", "forward"), zap.String("outcome", "error"), zap.Error(stripURLError(err)))
		return "Geocoding service is unreachable right now.", nil
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		s.publishGeocode(ctx, "error")
		s.log().Warn("geocode failed", zap.String("direction", "forward"), zap.String("outcome", "error"), zap.Error(stripURLError(err)))
		return "Geocoding response could not be read.", nil
	}
	if resp.StatusCode != http.StatusOK {
		s.publishGeocode(ctx, "error")
		s.log().Warn("geocode failed",
			zap.String("direction", "forward"),
			zap.String("outcome", "error"),
			zap.Int("status_code", resp.StatusCode))
		return "Geocoding service returned an error.", nil
	}

	// Field whitelist: parse only what we use; Nominatim returns lat/lon as strings.
	var results []struct {
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(body, &results); err != nil {
		// A malformed 200 body is a provider problem, not a genuine no-hit.
		s.publishGeocode(ctx, "error")
		s.log().Warn("geocode failed", zap.String("direction", "forward"), zap.String("outcome", "error"), zap.Error(err))
		return "Geocoding response could not be parsed.", nil
	}
	if len(results) == 0 {
		s.publishGeocode(ctx, "ok")
		return fmt.Sprintf("No coordinates found for %q.", query), nil
	}

	lat, latErr := strconv.ParseFloat(results[0].Lat, 64)
	lon, lonErr := strconv.ParseFloat(results[0].Lon, 64)
	if latErr != nil || lonErr != nil || channel.ValidateCoords(lat, lon) != nil {
		s.publishGeocode(ctx, "error")
		s.log().Warn("geocode failed", zap.String("direction", "forward"), zap.String("outcome", "error"))
		return "Geocoder returned unusable coordinates.", nil
	}

	s.publishGeocode(ctx, "ok")
	s.log().Debug("geocode ok",
		zap.String("direction", "forward"),
		zap.Int64("duration_ms", time.Since(start).Milliseconds()))
	name := results[0].DisplayName
	if runes := []rune(name); len(runes) > 256 {
		name = string(runes[:256])
	}
	return fmt.Sprintf("%s — %s (data © OpenStreetMap contributors)",
		name, channel.FormatCoords(lat, lon)), nil
}

// publishGeocode emits the geocode observability event (nil-bus safe).
func (s *Skill) publishGeocode(ctx context.Context, outcome string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, eventbus.Event{
		Type:    eventbus.GeocodeExecuted,
		Payload: eventbus.GeocodePayload{Direction: "forward", Outcome: outcome},
	})
}
