package mcpclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/iulita-ai/iulita/internal/httpfetch"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosida95/uritemplate/v3"
)

func safeOrigin(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}
func validateEndpoint(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 4096 || strings.ContainsAny(raw, "\r\n") {
		return nil, ErrInvalid
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, ErrDenied
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return nil, ErrDenied
	}
	if u.Port() != "" && u.Port() != "443" {
		return nil, ErrDenied
	}
	for k := range u.Query() {
		low := strings.ToLower(k)
		if strings.Contains(low, "token") || strings.Contains(low, "secret") || strings.Contains(low, "key") || strings.Contains(low, "password") {
			return nil, ErrInvalid
		}
	}
	return u, nil
}
func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "168.63.129.16/32", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "::ffff:0:0:0/96", "2001::/23", "2001:db8::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48", "fec0::/10"} {
		_, n, parseErr := net.ParseCIDR(cidr)
		if parseErr != nil {
			return false
		}
		if n.Contains(ip) {
			return false
		}
	}
	return true
}
func validateHeaders(h map[string]string) error {
	if len(h) > 8 {
		return ErrInvalid
	}
	for k, v := range h {
		if !strings.HasPrefix(strings.ToLower(k), "x-") || len(k) > 64 || len(v) > 16384 || strings.ContainsAny(k+v, "\r\n") || strings.ContainsAny(k, " ()<>@,;:\\[]?={}\t") {
			return ErrInvalid
		}
	}
	return nil
}

// validJSON rejects duplicate keys and deep input before decoding to Go values.
func validJSON(b []byte, limit int) error {
	if len(b) > limit || !json.Valid(b) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return ErrInvalid
		}
		tok, tokenErr := d.Token()
		if tokenErr != nil {
			return ErrInvalid
		}
		del, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch del {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, keyErr := d.Token()
				if keyErr != nil {
					return ErrInvalid
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return ErrInvalid
				}
				seen[s] = true
				if e := walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return ErrInvalid
		}
		_, endErr := d.Token()
		return endErr
	}
	return walk(0)
}
func schema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	if validJSON(raw, 64<<10) != nil {
		return nil, ErrInvalid
	}
	// The validator has no network Loader. Reject recursive and expensive schemas
	// rather than allowing remote schemas to amplify validation work.
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, ErrInvalid
	}
	nodes := 0
	var check func(any) bool
	check = func(x any) bool {
		nodes++
		if nodes > 1024 {
			return false
		}
		switch t := x.(type) {
		case map[string]any:
			for k, a := range t {
				if k == "$ref" || k == "$dynamicRef" {
					return false
				}
				if k == "pattern" || k == "patternProperties" {
					return false
				}
				if !check(a) {
					return false
				}
			}
		case []any:
			for _, a := range t {
				if !check(a) {
					return false
				}
			}
		}
		return true
	}
	if !check(v) {
		return nil, ErrInvalid
	}
	var s jsonschema.Schema
	if json.Unmarshal(raw, &s) != nil {
		return nil, ErrInvalid
	}
	r, e := s.Resolve(nil)
	if e != nil {
		return nil, ErrInvalid
	}
	return r, nil
}
func validateArguments(it Item, args json.RawMessage) error {
	var desc map[string]json.RawMessage
	if json.Unmarshal(it.Descriptor, &desc) != nil {
		return ErrInvalid
	}
	var v any
	if json.Unmarshal(args, &v) != nil {
		return ErrInvalid
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ErrInvalid
	}
	if it.Kind == "tool" {
		r, e := schema(desc["inputSchema"])
		if e != nil {
			return e
		}
		if r.Validate(v) != nil {
			return ErrInvalid
		}
	}
	if it.Kind == "prompt" {
		var names []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		}
		if len(desc["arguments"]) > 0 && json.Unmarshal(desc["arguments"], &names) != nil {
			return ErrInvalid
		}

		allowed := map[string]bool{}
		for _, n := range names {
			allowed[n.Name] = true
			if n.Required {
				if _, ok := m[n.Name]; !ok {
					return ErrInvalid
				}
			}
		}
		for k, a := range m {
			if !allowed[k] {
				return ErrInvalid
			}
			if _, ok := a.(string); !ok {
				return ErrInvalid
			}
		}
	}
	if it.Kind == "resource_template" {
		if _, e := expandTemplate(it.URI, args); e != nil {
			return e
		}
	}
	if it.Kind == "resource" && len(m) != 0 {
		return ErrInvalid
	}
	return nil
}
func item(kind string, descriptor any) (Item, error) {
	raw, e := json.Marshal(descriptor)
	if e != nil || validJSON(raw, 64<<10) != nil {
		return Item{}, ErrInvalid
	}
	var d struct {
		Name        string          `json:"name"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		URI         string          `json:"uri"`
		Template    string          `json:"uriTemplate"`
		Input       json.RawMessage `json:"inputSchema"`
		Output      json.RawMessage `json:"outputSchema"`
	}
	if json.Unmarshal(raw, &d) != nil || d.Name == "" || len(d.Name) > 256 {
		return Item{}, ErrInvalid
	}
	if kind == "resource_template" {
		d.URI = d.Template
	}
	sum := sha256.Sum256(raw)
	key := sha256.Sum256([]byte(kind + "\x00" + d.Name + "\x00" + d.URI))
	it := Item{ID: hex.EncodeToString(key[:16]), Kind: kind, Name: d.Name, Title: d.Title, Description: d.Description, URI: d.URI, Revision: hex.EncodeToString(sum[:]), Descriptor: raw, Effect: "unknown"}
	if kind == "tool" {
		if _, e = schema(d.Input); e != nil {
			it.ErrorCode = "unsupported_schema"
		}
		if len(d.Output) > 0 {
			if _, e = schema(d.Output); e != nil {
				it.ErrorCode = "unsupported_output_schema"
			}
		}
	}
	if kind == "resource_template" {
		if _, err := uritemplate.New(d.URI); err != nil {
			it.ErrorCode = "unsupported_template"
		}
	}
	return it, nil
}

// SDKProtocol owns only owner/revision-scoped runtimes. Policy, credentials
// and operation admission remain in the control plane; calls are never replayed.
type SDKProtocol struct {
	pool      runtimePool
	Transport http.RoundTripper
	Stdio     func(context.Context, Connection) (sdk.Transport, error)
}

// NewSDKProtocol constructs a fixed-proxy client with no direct or environment fallback.
func NewSDKProtocol(proxy string) (*SDKProtocol, error) {
	t, e := httpfetch.NewProxyOnlyTransport(proxy, httpfetch.DefaultLimits())
	if e != nil {
		return nil, ErrUnavailable
	}
	t.MaxResponseHeaderBytes = 64 << 10
	return &SDKProtocol{Transport: &pinnedProxy{base: t}}, nil
}

type guardedTransport struct {
	base     http.RoundTripper
	endpoint string
	token    string
	headers  map[string]string
}
type boundedBody struct {
	io.ReadCloser
	left int64
}

// Read decodes one size-limited JSON-RPC frame.
func (b *boundedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, ErrUnavailable
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, e := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, e
}

// RoundTrip adds only connection-bound authentication and enforces response limits.
func (t *guardedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if safeOrigin(r.URL.String()) != safeOrigin(t.endpoint) {
		return nil, ErrDenied
	}
	if r.URL.String() != t.endpoint {
		return nil, ErrDenied
	}
	q := r.Clone(r.Context())
	q.Header = r.Header.Clone()
	if t.token != "" {
		q.Header.Set("Authorization", "Bearer "+t.token)
	}
	for k, v := range t.headers {
		q.Header.Set(k, v)
	}
	// All requests, including negotiation/termination, use the fixed proxy. No
	// redirect, cookie jar, transparent OAuth handler, env proxy or direct fallback.
	if !headerLimit(q.Header) {
		return nil, ErrInvalid
	}
	resp, e := t.base.RoundTrip(q)
	if e != nil {
		return nil, ErrUnavailable
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		closeQuietly(resp.Body)
		return nil, ErrDenied
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, left: 8 << 20}
	return resp, nil
}
func (p *SDKProtocol) open(ctx, lifetime context.Context, c Connection) (*sdk.ClientSession, error) {
	options := &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	client := sdk.NewClient(&sdk.Implementation{Name: "iulita", Version: "mcp-1"}, options)
	client.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, r sdk.Request) (sdk.Result, error) {
			switch method {
			case "roots/list", "sampling/createMessage", "elicitation/create":
				return nil, ErrDenied
			}
			return next(ctx, method, r)
		}
	})
	client.AddSendingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, r sdk.Request) (sdk.Result, error) {
			if method == "tools/call" || method == "resources/read" || method == "prompts/get" {
				before, ok := ctx.Value(dispatchKey{}).(func() error)
				if !ok || before == nil {
					return nil, ErrDenied
				}
				if e := before(); e != nil {
					return nil, e
				}
			}
			result, err := next(ctx, method, r)
			switch r := result.(type) {
			case *sdk.ListToolsResult:
				if r != nil {
					r.TTLMs = 0
					r.CacheScope = "private"
				}
			case *sdk.ListResourcesResult:
				if r != nil {
					r.TTLMs = 0
					r.CacheScope = "private"
				}
			case *sdk.ListResourceTemplatesResult:
				if r != nil {
					r.TTLMs = 0
					r.CacheScope = "private"
				}
			case *sdk.ListPromptsResult:
				if r != nil {
					r.TTLMs = 0
					r.CacheScope = "private"
				}
			case *sdk.ReadResourceResult:
				if r != nil {
					r.TTLMs = 0
					r.CacheScope = "private"
				}
			}
			return result, err
		}
	})
	var transport sdk.Transport
	if c.Transport == "stdio" {
		if p.Stdio == nil {
			return nil, ErrUnavailable
		}
		var e error
		transport, e = p.Stdio(lifetime, c)
		if e != nil {
			return nil, ErrUnavailable
		}
	} else {
		if _, e := validateEndpoint(c.Endpoint); e != nil {
			return nil, e
		}
		if p.Transport == nil {
			return nil, ErrUnavailable
		}
		t := &guardedTransport{base: p.Transport, endpoint: c.Endpoint, token: c.Token, headers: c.Headers}
		transport = &sdk.StreamableClientTransport{Endpoint: c.Endpoint, HTTPClient: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrDenied }}, DisableStandaloneSSE: true, MaxRetries: -1, MaxEventSize: 8 << 20}
	}
	session, e := client.Connect(ctx, transport, nil)
	if e != nil {
		return nil, ErrUnavailable
	}
	switch session.InitializeResult().ProtocolVersion {
	case "2026-07-28", "2025-11-25", "2025-06-18":
	default:
		closeQuietly(session)
		return nil, ErrUnavailable
	}
	return session, nil
}

// Discover collects a bounded complete catalog from one isolated owner runtime.
func (p *SDKProtocol) Discover(ctx context.Context, c Connection) ([]Item, error) {
	session, release, e := p.acquire(ctx, c)
	if e != nil {
		return nil, e
	}
	healthy := false
	defer func() { release(healthy) }()
	items, err := discoverSession(ctx, session)
	healthy = err == nil
	return items, err
}
func discoverSession(ctx context.Context, session *sdk.ClientSession) ([]Item, error) {
	caps := session.InitializeResult().Capabilities
	if caps == nil {
		return nil, ErrInvalid
	}
	out := []Item{}
	total := 0
	add := func(kind string, v any) error {
		it, er := item(kind, v)
		if er != nil {
			return er
		}
		total += len(it.Descriptor)
		if len(out) >= 1000 || total > 8<<20 {
			return ErrInvalid
		}
		for oldIndex := range out {
			old := &out[oldIndex]
			if old.ID == it.ID {
				return ErrInvalid
			}
		}
		out = append(out, it)
		return nil
	}
	if caps.Tools != nil {
		cursor := ""
		seen := map[string]bool{}
		for n := 0; ; n++ {
			if n >= 100 {
				return nil, ErrInvalid
			}
			r, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
			if err != nil {
				return nil, ErrUnavailable
			}
			for _, v := range r.Tools {
				if e := add("tool", v); e != nil {
					return nil, e
				}
			}
			cursor = r.NextCursor
			if cursor == "" {
				break
			}
			if seen[cursor] {
				return nil, ErrInvalid
			}
			seen[cursor] = true
		}
	}
	if caps.Resources != nil {
		cursor := ""
		seen := map[string]bool{}
		for n := 0; ; n++ {
			if n >= 100 {
				return nil, ErrInvalid
			}
			r, err := session.ListResources(ctx, &sdk.ListResourcesParams{Cursor: cursor})
			if err != nil {
				return nil, ErrUnavailable
			}
			for _, v := range r.Resources {
				if e := add("resource", v); e != nil {
					return nil, e
				}
			}
			cursor = r.NextCursor
			if cursor == "" {
				break
			}
			if seen[cursor] {
				return nil, ErrInvalid
			}
			seen[cursor] = true
		}
	}
	if caps.Resources != nil {
		cursor := ""
		seen := map[string]bool{}
		for n := 0; ; n++ {
			if n >= 100 {
				return nil, ErrInvalid
			}
			r, err := session.ListResourceTemplates(ctx, &sdk.ListResourceTemplatesParams{Cursor: cursor})
			if err != nil {
				return nil, ErrUnavailable
			}
			for _, v := range r.ResourceTemplates {
				if e := add("resource_template", v); e != nil {
					return nil, e
				}
			}
			cursor = r.NextCursor
			if cursor == "" {
				break
			}
			if seen[cursor] {
				return nil, ErrInvalid
			}
			seen[cursor] = true
		}
	}
	if caps.Prompts != nil {
		cursor := ""
		seen := map[string]bool{}
		for n := 0; ; n++ {
			if n >= 100 {
				return nil, ErrInvalid
			}
			r, err := session.ListPrompts(ctx, &sdk.ListPromptsParams{Cursor: cursor})
			if err != nil {
				return nil, ErrUnavailable
			}
			for _, v := range r.Prompts {
				if e := add("prompt", v); e != nil {
					return nil, e
				}
			}
			cursor = r.NextCursor
			if cursor == "" {
				break
			}
			if seen[cursor] {
				return nil, ErrInvalid
			}
			seen[cursor] = true
		}
	}
	return out, nil
}

// Invoke calls a revision-matched descriptor through a revalidated dispatch boundary.
func (p *SDKProtocol) Invoke(ctx context.Context, c Connection, it Item, args json.RawMessage, before func() error) (payload json.RawMessage, toolError bool, callErr error) {
	session, release, e := p.acquire(ctx, c)
	if e != nil {
		return nil, false, e
	}
	healthy := false
	defer func() { release(healthy) }()
	ctx = context.WithValue(ctx, dispatchKey{}, before)
	// Refresh under the same auth/session before dispatch. This also populates
	// the SDK cache needed for x-mcp-header processing on modern HTTP.
	fresh, discoveryErr := discoverSession(ctx, session)
	if discoveryErr != nil {
		return nil, false, discoveryErr
	}
	matched := false
	for candidateIndex := range fresh {
		candidate := &fresh[candidateIndex]
		if candidate.ID == it.ID && candidate.Revision == it.Revision && candidate.ErrorCode == "" {
			matched = true
		}
	}
	if !matched {
		return nil, false, ErrConflict
	}
	var value any
	isError := false
	switch it.Kind {
	case "tool":
		r, err := session.CallTool(ctx, &sdk.CallToolParams{Name: it.Name, Arguments: args})
		if err != nil {
			return nil, false, ErrUnavailable
		}
		if r.NeedsInput() {
			return nil, false, ErrUnavailable
		}
		value = r
		isError = r.IsError
		var d struct {
			Output json.RawMessage `json:"outputSchema"`
		}
		if json.Unmarshal(it.Descriptor, &d) != nil {
			return nil, false, ErrInvalid
		}
		if len(d.Output) > 0 && !isError {
			resolved, er := schema(d.Output)
			if er != nil || resolved.Validate(r.StructuredContent) != nil {
				return nil, false, ErrInvalid
			}
		}
	case "resource", "resource_template":
		uri := it.URI
		if it.Kind == "resource_template" {
			var err error
			uri, err = expandTemplate(it.URI, args)
			if err != nil {
				return nil, false, err
			}
		}
		r, err := session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
		if err != nil {
			return nil, false, ErrUnavailable
		}
		value = r
	case "prompt":
		var params map[string]string
		if json.Unmarshal(args, &params) != nil {
			return nil, false, ErrInvalid
		}
		r, err := session.GetPrompt(ctx, &sdk.GetPromptParams{Name: it.Name, Arguments: params})
		if err != nil {
			return nil, false, ErrUnavailable
		}
		value = r
	default:
		return nil, false, ErrInvalid
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false, ErrInvalid
	}
	var resultKind struct {
		Type string `json:"resultType"`
	}
	if json.Unmarshal(raw, &resultKind) != nil || resultKind.Type != "" && resultKind.Type != "complete" {
		return nil, false, ErrUnavailable
	}
	if validJSON(raw, 32<<20) != nil {
		return nil, false, ErrInvalid
	}
	healthy = true
	return raw, isError, nil
}

// No custom header can turn a schema parameter into an authentication header.
// SDK v1.8 implements x-mcp-header encoding; schema validation rejects malformed
// annotations before descriptors become callable.
func headerLimit(h http.Header) bool {
	size := 0
	for k, v := range h {
		size += len(k) + len(strings.Join(v, ""))
	}
	return size <= 64<<10
}

func expandTemplate(template string, args json.RawMessage) (string, error) {
	t, e := uritemplate.New(template)
	if e != nil {
		return "", ErrInvalid
	}
	var params map[string]any
	if json.Unmarshal(args, &params) != nil {
		return "", ErrInvalid
	}
	values := uritemplate.Values{}
	allowed := map[string]bool{}
	for _, v := range t.Varnames() {
		allowed[v] = true
	}
	for k, v := range params {
		s, ok := v.(string)
		if !ok || !allowed[k] || len(s) > 4096 {
			return "", ErrInvalid
		}
		values.Set(k, uritemplate.String(s))
	}
	for k := range allowed {
		if _, ok := params[k]; !ok {
			return "", ErrInvalid
		}
	}
	uri, e := t.Expand(values)
	if e != nil || len(uri) > 4096 {
		return "", ErrInvalid
	}
	return uri, nil
}
