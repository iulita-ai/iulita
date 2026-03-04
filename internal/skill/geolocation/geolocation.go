package geolocation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

const (
	// IP detection endpoints.
	ipifyURL   = "https://api.ipify.org?format=json"
	icanhazURL = "https://icanhazip.com"

	// Geolocation endpoints.
	ipAPIURL  = "http://ip-api.com/json/" // free tier requires HTTP
	ipapiURL  = "https://ipapi.co/"       // fallback
	ipinfoURL = "https://ipinfo.io/"      // paid/free-tier with token

	maxResponseSize = 64 * 1024 // 64 KB
	userAgent       = "iulita-bot/1.0"
)

// Skill provides IP geolocation lookups.
type Skill struct {
	httpClient *http.Client
	mu         sync.RWMutex
	apiKey     string // ipinfo.io token (optional)
}

// New creates a new geolocation skill.
func New(httpClient *http.Client) *Skill {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Skill{httpClient: httpClient}
}

func (s *Skill) Name() string { return "geolocation" }

func (s *Skill) Description() string {
	return "Determine the user's public IP address and geographic location (country, city, timezone, ISP). " +
		"Can also look up location for a specific IP address."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"ip": {
			"type": "string",
			"description": "IP address to look up. If omitted, auto-detects the user's public IP."
		}
	}
}`)
}

// OnConfigChanged implements skill.ConfigReloadable.
func (s *Skill) OnConfigChanged(key, value string) {
	if key != "skills.geolocation.api_key" {
		return
	}
	s.mu.Lock()
	s.apiKey = value
	s.mu.Unlock()
}

type geoInput struct {
	IP string `json:"ip"`
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

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in geoInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
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
