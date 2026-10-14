package httpfetch

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ValidationError is a pre-network input rejection. It carries no secrets.
type ValidationError struct {
	StepID  string
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.StepID != "" {
		return fmt.Sprintf("step %s: %s: %s", e.StepID, e.Field, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func validationErr(step, field, format string, args ...any) error {
	return &ValidationError{StepID: step, Field: field, Message: fmt.Sprintf(format, args...)}
}

// validateStepID checks the ASCII token form (≤64 chars).
func validateStepID(id string) error {
	if id == "" {
		return fmt.Errorf("step id is required")
	}
	if len(id) > 64 {
		return fmt.Errorf("step id too long (max 64)")
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("step id must be an ASCII token of [a-zA-Z0-9_-], got %q", id)
		}
	}
	return nil
}

// ValidatePlan performs the full semantic validation pass. It must complete
// before any network activity: unknown options, forward references, and
// conflicting fields are rejected here.
func (e *Engine) ValidatePlan(plan Plan) error {
	if plan.Version != PlanVersion {
		return validationErr("", "version", "must be %d, got %d", PlanVersion, plan.Version)
	}
	if !plan.CookieMode.valid() {
		return validationErr("", "cookie_mode", "must be none or chain")
	}
	hasRequest := plan.Request != nil
	hasSteps := len(plan.Steps) > 0
	if hasRequest == hasSteps {
		return validationErr("", "plan", "exactly one of request or steps is required")
	}
	if plan.Request != nil {
		r := *plan.Request
		if r.ID == "" {
			r.ID = defaultStepID
		}
		return e.validateRequest(r, nil, plan.CookieMode)
	}
	if len(plan.Steps) > e.limits.MaxChainSteps {
		return validationErr("", "steps", "chain exceeds max steps %d", e.limits.MaxChainSteps)
	}
	seenIDs := map[string]bool{}
	declared := map[string]bool{}
	for i := range plan.Steps {
		r := plan.Steps[i]
		id := r.ID
		if id == "" {
			return validationErr("", fmt.Sprintf("steps[%d].id", i), "id is required in chains")
		}
		if err := validateStepID(id); err != nil {
			return validationErr(id, "id", "%s", err)
		}
		if seenIDs[id] {
			return validationErr(id, "id", "duplicate step id")
		}
		seenIDs[id] = true
		if err := e.validateRequest(r, declared, plan.CookieMode); err != nil {
			return err
		}
		for _, ex := range r.Extract {
			declared[ex.Name] = true
		}
	}
	return nil
}

const defaultStepID = "request"

// validateRequest validates one step. knownVars holds variable names declared
// by PREVIOUS steps only (forward references are rejected).
func (e *Engine) validateRequest(r Request, knownVars map[string]bool, cookieMode CookieMode) error {
	id := r.ID
	if id == "" {
		id = defaultStepID
	}

	method, err := validateMethod(r.Method)
	if err != nil {
		return validationErr(id, "method", "%s", err)
	}
	_ = method

	hasURL := r.URL != ""
	hasURLFrom := r.URLFrom != nil
	if hasURL == hasURLFrom {
		return validationErr(id, "url", "exactly one of url or url_from is required")
	}
	if hasURL {
		if err := validateBaseURL(r.URL, len(r.PathSegments) > 0); err != nil {
			return validationErr(id, "url", "%s", err)
		}
	} else {
		if r.URLFrom.Step == "" {
			return validationErr(id, "url_from.step", "source step is required")
		}
		if r.URLFrom.Step == id {
			return validationErr(id, "url_from.step", "cannot reference its own step")
		}
		if err := validateHeaderName(r.URLFrom.Header); err != nil {
			return validationErr(id, "url_from.header", "%s", err)
		}
		if r.URLFrom.Index < 0 {
			return validationErr(id, "url_from.index", "must be >= 0")
		}
	}

	for i, seg := range r.PathSegments {
		switch seg.Kind {
		case ValueLiteral:
			if strings.ContainsAny(seg.Literal, "\x00\r\n") {
				return validationErr(id, fmt.Sprintf("path_segments[%d]", i), "control characters not allowed")
			}
		case ValueVar:
			if !knownVars[seg.Ref] {
				return validationErr(id, fmt.Sprintf("path_segments[%d]", i), "unknown variable %q (forward references are rejected)", seg.Ref)
			}
		case ValueCredential:
			return validationErr(id, fmt.Sprintf("path_segments[%d]", i), "credential refs are not allowed in path segments")
		}
	}

	if r.Host != "" && hasURL {
		if err := validateHostField(r.Host, r.URL); err != nil {
			return validationErr(id, "host", "%s", err)
		}
	}

	for i, q := range r.Query {
		if strings.TrimSpace(q.Name) == "" {
			return validationErr(id, fmt.Sprintf("query[%d].name", i), "name is required")
		}
		if strings.ContainsAny(q.Name, "\x00\r\n") {
			return validationErr(id, fmt.Sprintf("query[%d].name", i), "control characters not allowed")
		}
		if err := e.validateValueRef(id, fmt.Sprintf("query[%d].value", i), q.Value, knownVars, true); err != nil {
			return err
		}
	}

	hasAuth := r.Auth != nil && r.Auth.Type != AuthNone && r.Auth.Type != ""
	hasCookieJar := planCookieChain(cookieMode)
	seenHeaders := map[string]bool{}
	var headerBytes int
	for i, h := range r.Headers {
		lower, err := validateHeaderSpec(h, hasAuth, hasCookieJar)
		if err != nil {
			return validationErr(id, fmt.Sprintf("headers[%d]", i), "%s", err)
		}
		if seenHeaders[lower] {
			return validationErr(id, fmt.Sprintf("headers[%d].name", i), "duplicate header %q; merge values into one entry", h.Name)
		}
		seenHeaders[lower] = true
		headerBytes += len(h.Name) + 8
		for _, v := range h.Values {
			headerBytes += len(v.Literal) + 8
			if err := e.validateValueRef(id, fmt.Sprintf("headers[%s]", lower), v, knownVars, false); err != nil {
				return err
			}
		}
		// Content-Type is engine-managed for structured body encodings.
		if lower == "content-type" {
			switch bodyType(r) {
			case BodyJSON, BodyForm, BodyMultipart:
				return validationErr(id, fmt.Sprintf("headers[%d].name", i), "content-type conflicts with body type %q (the engine sets it)", bodyType(r))
			}
		}
	}
	if headerBytes > int(e.limits.HeaderBlockBytes) {
		return validationErr(id, "headers", "header block exceeds %d bytes", e.limits.HeaderBlockBytes)
	}
	if len(r.Headers) > e.limits.MaxHeaderFields {
		return validationErr(id, "headers", "too many header fields (max %d)", e.limits.MaxHeaderFields)
	}

	if r.Auth != nil {
		switch r.Auth.Type {
		case "", AuthNone:
			// ok
		case AuthBasic:
			if r.Auth.CredentialRef == "" && strings.Contains(r.Auth.Username, ":") {
				return validationErr(id, "auth.username", "basic usernames must not contain ':'")
			}
		case AuthBearer:
			// ok
		default:
			return validationErr(id, "auth.type", "must be none|basic|bearer")
		}
	}

	if r.Body != nil {
		if method == "HEAD" {
			return validationErr(id, "body", "HEAD requests cannot carry a body")
		}
		if err := e.validateBody(id, r.Body, knownVars); err != nil {
			return err
		}
	}

	if !r.Redirects.valid() {
		return validationErr(id, "redirects", "must be none or same_origin")
	}

	if r.TimeoutMs < 0 {
		return validationErr(id, "timeout_ms", "must be >= 0")
	}
	if r.TimeoutMs > 0 && r.TimeoutMs > e.limits.RequestTimeout.Milliseconds() {
		return validationErr(id, "timeout_ms", "exceeds server maximum %dms", e.limits.RequestTimeout.Milliseconds())
	}

	if r.Response != nil {
		if !r.Response.Mode.valid() {
			return validationErr(id, "response.mode", "must be inline, download, or discard")
		}
		if r.Response.InlineMaxBytes < 0 {
			return validationErr(id, "response.inline_max_bytes", "must be >= 0")
		}
		if r.Response.InlineMaxBytes > e.limits.InlineMaxBytes {
			return validationErr(id, "response.inline_max_bytes", "exceeds server cap %d", e.limits.InlineMaxBytes)
		}
		if strings.ContainsAny(r.Response.Filename, "/\\\x00") {
			return validationErr(id, "response.filename", "must be a display name without path separators")
		}
	}

	for i, m := range r.ExpectStatus {
		if m.From < 100 || m.To > 599 || m.From > m.To {
			return validationErr(id, fmt.Sprintf("expect_status[%d]", i), "invalid status range %d-%d", m.From, m.To)
		}
	}

	seenVars := map[string]bool{}
	for i, ex := range r.Extract {
		if err := validateStepID(ex.Name); err != nil {
			return validationErr(id, fmt.Sprintf("extract[%d].name", i), "%s", err)
		}
		if seenVars[ex.Name] {
			return validationErr(id, fmt.Sprintf("extract[%d].name", i), "duplicate variable %q", ex.Name)
		}
		seenVars[ex.Name] = true
		switch ex.Type {
		case "", VarString, VarNumber, VarInteger, VarBoolean:
		default:
			return validationErr(id, fmt.Sprintf("extract[%d].type", i), "must be string|number|integer|boolean")
		}
		if ex.Source == ExtractJSONPointer && !strings.HasPrefix(ex.Pointer, "/") && ex.Pointer != "" {
			return validationErr(id, fmt.Sprintf("extract[%d].pointer", i), "JSON pointer must start with '/'")
		}
		if len(ex.Default) > 0 && !json.Valid(ex.Default) {
			return validationErr(id, fmt.Sprintf("extract[%d].default", i), "default must be valid JSON")
		}
	}
	return nil
}

// planCookieChain reports whether the request runs under a chain cookie jar.
// Single requests never get a jar (v1).
func planCookieChain(mode CookieMode) bool {
	return mode == CookieModeChain
}

// validateValueRef checks one ValueExpr in context.
func (e *Engine) validateValueRef(stepID, field string, v ValueExpr, knownVars map[string]bool, isQuery bool) error {
	switch v.Kind {
	case ValueLiteral:
		return nil
	case ValueVar:
		if !knownVars[v.Ref] {
			return validationErr(stepID, field, "unknown variable %q (forward references are rejected)", v.Ref)
		}
	case ValueCredential:
		if !isQuery {
			return nil // header/form secret values allowed
		}
		// Query values travel in URLs (logs, Referer): allowed only with the
		// explicit sensitive-egress consent, enforced at execution time.
		return nil
	}
	return nil
}

// validateBody applies body-specific rules.
func (e *Engine) validateBody(id string, b *BodySpec, knownVars map[string]bool) error {
	switch b.Type {
	case BodyText:
		if int64(len(b.Value)) > e.limits.MaxUploadBytes {
			return validationErr(id, "body.value", "text body exceeds upload cap %d bytes", e.limits.MaxUploadBytes)
		}
	case BodyBase64:
		raw, err := base64.StdEncoding.DecodeString(b.Value)
		if err != nil {
			return validationErr(id, "body.value", "invalid base64: %v", err)
		}
		if int64(len(raw)) > e.limits.MaxUploadBytes {
			return validationErr(id, "body.value", "binary body exceeds upload cap %d bytes", e.limits.MaxUploadBytes)
		}
	case BodyJSON:
		refs, err := walkJSONRefs(b.RawJSON)
		if err != nil {
			return validationErr(id, "body.value", "%s", err)
		}
		total := 0
		for _, ref := range refs {
			total += len(ref.Value)
			if ref.IsVar && !knownVars[ref.Value] {
				return validationErr(id, "body.value", "unknown variable %q (forward references are rejected)", ref.Value)
			}
		}
		if total > 64*1024 {
			return validationErr(id, "body.value", "too many reference bindings in JSON body")
		}
	case BodyForm:
		var total int64
		for i, f := range b.Fields {
			if strings.TrimSpace(f.Name) == "" {
				return validationErr(id, fmt.Sprintf("body.fields[%d].name", i), "name is required")
			}
			if err := e.validateValueRef(id, fmt.Sprintf("body.fields[%d].value", i), f.Value, knownVars, false); err != nil {
				return err
			}
			total += int64(len(f.Name) + len(f.Value.Literal) + 4)
		}
		if total > e.limits.MaxUploadBytes {
			return validationErr(id, "body.fields", "form body exceeds upload cap %d bytes", e.limits.MaxUploadBytes)
		}
	case BodyMultipart:
		for i, p := range b.Parts {
			if strings.TrimSpace(p.Name) == "" {
				return validationErr(id, fmt.Sprintf("body.parts[%d].name", i), "name is required")
			}
			if p.ArtifactRef != "" && p.Value != "" {
				return validationErr(id, fmt.Sprintf("body.parts[%d]", i), "part cannot be both text and artifact")
			}
			if strings.ContainsAny(p.Filename, "/\\\x00") {
				return validationErr(id, fmt.Sprintf("body.parts[%d].filename", i), "must be a display name without path separators")
			}
		}
	case BodyArtifact:
		if b.ArtifactRef == "" {
			return validationErr(id, "body.artifact_ref", "artifact reference is required")
		}
	default:
		return validationErr(id, "body.type", "unsupported body type")
	}
	return nil
}

// validateBaseURL enforces: absolute HTTP(S), no userinfo, no fragment;
// with path_segments the path must end in '/' and the URL must carry no query.
func validateBaseURL(raw string, hasSegments bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL has no host")
	}
	if u.User != nil {
		return fmt.Errorf("userinfo in URLs is not allowed")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return fmt.Errorf("fragments are not sent and are not allowed in requests")
	}
	if hasSegments {
		if u.RawQuery != "" {
			return fmt.Errorf("URL must not carry a query when path_segments are used (use the query field)")
		}
		if !strings.HasSuffix(u.EscapedPath(), "/") {
			return fmt.Errorf("base URL path must end with '/' when path_segments are used")
		}
	}
	if strings.ContainsAny(u.Host, " \t\x00\r\n") {
		return fmt.Errorf("invalid host characters")
	}
	return nil
}

// validateHostField enforces the v1 rule: host must equal the URL authority.
func validateHostField(host, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if !strings.EqualFold(strings.TrimSuffix(host, "/"), u.Host) {
		return fmt.Errorf("host must equal the URL authority %q in v1 (virtual hosts are not supported)", u.Host)
	}
	return nil
}

// JSONRef is a reference found inside a JSON body.
type JSONRef struct {
	Value string
	IsVar bool // false → credential_ref
	Path  string
}

// walkJSONRefs validates the JSON body reference structure and returns all
// references. Objects that contain $var or credential_ref must be exactly
// that reference; {"$literal": <any>} escapes evaluation of its inner value.
func walkJSONRefs(raw json.RawMessage) ([]JSONRef, error) {
	var refs []JSONRef
	if err := walkJSONValue(raw, "$", &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

func walkJSONValue(raw json.RawMessage, path string, refs *[]JSONRef) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return fmt.Errorf("at %s: %w", path, err)
		}
		_, hasVar := obj["$var"]
		_, hasCred := obj["credential_ref"]
		if hasVar || hasCred {
			if len(obj) != 1 {
				return fmt.Errorf("at %s: reference objects must contain exactly one key", path)
			}
			var refStr string
			isVar := hasVar
			src := obj["$var"]
			if hasCred {
				src = obj["credential_ref"]
			}
			if err := json.Unmarshal(src, &refStr); err != nil {
				return fmt.Errorf("at %s: reference must be a string: %w", path, err)
			}
			if refStr == "" {
				return fmt.Errorf("at %s: reference name must not be empty", path)
			}
			*refs = append(*refs, JSONRef{Value: refStr, IsVar: isVar, Path: path})
			return nil
		}
		if lit, hasLiteral := obj["$literal"]; hasLiteral {
			if len(obj) != 1 {
				return fmt.Errorf("at %s: $literal objects must contain exactly one key", path)
			}
			_ = lit // inner value is taken verbatim, never evaluated
			return nil
		}
		for k, v := range obj {
			if strings.HasPrefix(k, "$") && k != "$literal" {
				return fmt.Errorf("at %s: unknown reserved key %q", path, k)
			}
			if err := walkJSONValue(v, path+"/"+escapeJSONPointerToken(k), refs); err != nil {
				return err
			}
		}
		return nil
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return fmt.Errorf("at %s: %w", path, err)
		}
		for i, v := range arr {
			if err := walkJSONValue(v, fmt.Sprintf("%s/%d", path, i), refs); err != nil {
				return err
			}
		}
		return nil
	}
	return nil // scalar
}

func escapeJSONPointerToken(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}
