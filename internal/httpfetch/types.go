// Package httpfetch implements the strict HTTP execution engine behind the
// http_request tool: typed request/result contracts, proxy-only transport,
// policy checks, chains with variable extraction, and bounded downloads.
//
// The engine never imports assistant, channel, skillmgr, or concrete storage
// packages; all host dependencies are narrow interfaces defined here and
// wired by cmd/iulita/main.go.
package httpfetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// PlanVersion is the only supported input contract version.
const PlanVersion = 1

// Plan is a validated execution plan: exactly one request or a step chain.
// The JSON input from the model is closed (unknown fields/enums rejected
// before any network activity).
type Plan struct {
	Version    int        `json:"version"`
	Request    *Request   `json:"request,omitempty"`
	Steps      []Request  `json:"steps,omitempty"`
	CookieMode CookieMode `json:"cookie_mode,omitempty"`
}

// CookieMode controls the lifetime of the cookie jar.
type CookieMode string

const (
	// CookieModeNone disables cookies (default).
	CookieModeNone CookieMode = "none"
	// CookieModeChain keeps a jar for the duration of one run.
	CookieModeChain CookieMode = "chain"
)

func (c CookieMode) valid() bool { return c == "" || c == CookieModeNone || c == CookieModeChain }

// Request describes one HTTP step. Field semantics follow docs_ext plan §4.1.
type Request struct {
	ID              string        `json:"id,omitempty"`
	Method          string        `json:"method,omitempty"`
	URL             string        `json:"url,omitempty"`
	URLFrom         *URLFrom      `json:"url_from,omitempty"`
	PathSegments    []ValueExpr   `json:"path_segments,omitempty"`
	Host            string        `json:"host,omitempty"`
	Query           []QueryParam  `json:"query,omitempty"`
	Headers         []HeaderSpec  `json:"headers,omitempty"`
	Auth            *AuthSpec     `json:"auth,omitempty"`
	Body            *BodySpec     `json:"body,omitempty"`
	Redirects       RedirectMode  `json:"redirects,omitempty"`
	TimeoutMs       int64         `json:"timeout_ms,omitempty"`
	Response        *ResponseSpec `json:"response,omitempty"`
	ExpectStatus    []StatusMatch `json:"expect_status,omitempty"`
	ContinueOnError bool          `json:"continue_on_http_error,omitempty"`
	Extract         []ExtractSpec `json:"extract,omitempty"`
}

// URLFrom resolves the step URL from a previous step's response header
// (typically Location), passed through full policy/SSRF control.
type URLFrom struct {
	Step   string `json:"step"`
	Header string `json:"header"`
	Index  int    `json:"index"`
}

// RedirectMode controls automatic redirect following.
type RedirectMode string

const (
	// RedirectNone returns 3xx responses to the caller (default).
	RedirectNone RedirectMode = "none"
	// RedirectSameOrigin follows hops on the same exact origin.
	RedirectSameOrigin RedirectMode = "same_origin"
)

func (r RedirectMode) valid() bool { return r == "" || r == RedirectNone || r == RedirectSameOrigin }

// ValueKind discriminates ValueExpr variants.
type ValueKind int

const (
	// ValueLiteral is a plain string value.
	ValueLiteral ValueKind = iota
	// ValueVar references an extracted variable.
	ValueVar ValueKind = iota + 1
	// ValueCredential references a stored credential.
	ValueCredential ValueKind = iota + 2
)

// ValueExpr is a string literal or a typed reference usable in query values,
// header values, form values, and single path segments. Secret refs are
// rejected in path segments and gated by egress policy in query values.
type ValueExpr struct {
	Kind    ValueKind
	Literal string
	Ref     string
}

func (v ValueExpr) String() string {
	if v.Kind == ValueLiteral {
		return v.Literal
	}
	return v.Ref
}

// UnmarshalJSON accepts a JSON string (literal) or an object that is exactly
// {"$var":"name"} or {"credential_ref":"name"}. Anything else is an error.
func (v *ValueExpr) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("value: %w", err)
		}
		*v = ValueExpr{Kind: ValueLiteral, Literal: s}
		return nil
	}
	var ref struct {
		Var           *string `json:"$var"`
		CredentialRef *string `json:"credential_ref"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ref); err != nil {
		return fmt.Errorf("value must be a string or {\"$var\"|\"credential_ref\"} object: %w", err)
	}
	if extra := trailingJSON(dec); extra {
		return fmt.Errorf("value object must contain exactly one reference")
	}
	switch {
	case ref.Var != nil && ref.CredentialRef == nil:
		*v = ValueExpr{Kind: ValueVar, Ref: *ref.Var}
	case ref.CredentialRef != nil && ref.Var == nil:
		*v = ValueExpr{Kind: ValueCredential, Ref: *ref.CredentialRef}
	default:
		return fmt.Errorf("value object must contain exactly one of $var/credential_ref")
	}
	return nil
}

// MarshalJSON renders a literal back as a string; references as their object
// form. Used for canonical plan fingerprinting, never for secrets in logs.
func (v ValueExpr) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case ValueLiteral:
		return json.Marshal(v.Literal)
	case ValueVar:
		return json.Marshal(map[string]string{"$var": v.Ref})
	default:
		return json.Marshal(map[string]string{"credential_ref": v.Ref})
	}
}

// QueryParam is a repeated query parameter entry (order preserved).
type QueryParam struct {
	Name  string    `json:"name"`
	Value ValueExpr `json:"value"`
}

// HeaderSpec carries repeated header values in declared order.
type HeaderSpec struct {
	Name   string      `json:"name"`
	Values []ValueExpr `json:"values"`
}

// AuthType discriminates AuthSpec variants.
type AuthType string

const (
	// AuthNone disables authentication.
	AuthNone AuthType = "none"
	// AuthBasic selects HTTP Basic credentials.
	AuthBasic AuthType = "basic"
	// AuthBearer selects bearer-token credentials.
	AuthBearer AuthType = "bearer"
)

// AuthSpec is the closed auth union:
// {type:none} | {type:basic,credential_ref} | {type:basic,username,password} |
// {type:bearer,credential_ref} | {type:bearer,token}.
type AuthSpec struct {
	Type          AuthType `json:"type"`
	CredentialRef string   `json:"credential_ref,omitempty"`
	Username      string   `json:"username,omitempty"`
	Password      string   `json:"password,omitempty"`
	Token         string   `json:"token,omitempty"`
}

// UnmarshalJSON enforces the closed union: credential and inline forms are
// mutually exclusive, and each type accepts only its own fields.
func (a *AuthSpec) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type          string  `json:"type"`
		CredentialRef *string `json:"credential_ref"`
		Username      *string `json:"username"`
		Password      *string `json:"password"`
		Token         *string `json:"token"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if trailingJSON(dec) {
		return fmt.Errorf("auth: unexpected trailing data")
	}
	a.Type = AuthType(raw.Type)
	switch a.Type {
	case AuthNone:
		if raw.CredentialRef != nil || raw.Username != nil || raw.Password != nil || raw.Token != nil {
			return fmt.Errorf("auth type %q accepts no additional fields", a.Type)
		}
	case AuthBasic:
		hasCred := raw.CredentialRef != nil
		hasInline := raw.Username != nil || raw.Password != nil
		if hasCred == hasInline {
			return fmt.Errorf("auth basic requires either credential_ref or username+password")
		}
		if hasCred {
			a.CredentialRef = *raw.CredentialRef
		} else {
			if raw.Username == nil || raw.Password == nil {
				return fmt.Errorf("auth basic inline form requires both username and password")
			}
			a.Username, a.Password = *raw.Username, *raw.Password
		}
	case AuthBearer:
		hasCred := raw.CredentialRef != nil
		hasInline := raw.Token != nil
		if hasCred == hasInline {
			return fmt.Errorf("auth bearer requires either credential_ref or token")
		}
		if hasCred {
			a.CredentialRef = *raw.CredentialRef
		} else {
			a.Token = *raw.Token
		}
	default:
		return fmt.Errorf("auth type must be one of none|basic|bearer, got %q", raw.Type)
	}
	return nil
}

// BodyType discriminates BodySpec variants.
type BodyType string

const (
	// BodyText is a UTF-8 text body.
	BodyText BodyType = "text"
	// BodyJSON is a JSON body with typed references.
	BodyJSON BodyType = "json"
	// BodyBase64 is an arbitrary-bytes body.
	BodyBase64 BodyType = "base64"
	// BodyForm is a urlencoded form body.
	BodyForm BodyType = "form"
	// BodyMultipart is a multipart body with text/artifact parts.
	BodyMultipart BodyType = "multipart"
	// BodyArtifact uploads a stored artifact.
	BodyArtifact BodyType = "artifact"
)

// BodySpec is the tagged body union. For text/base64 the payload key is
// `value`; form uses `fields`; multipart uses `parts`; artifact uses
// `artifact_ref`. The json `value` is kept raw: references inside it are
// resolved by the engine, never by re-encoding the model's JSON blindly.
type BodySpec struct {
	Type        BodyType        `json:"type"`
	Value       string          `json:"value,omitempty"`        // text | base64
	JSONValue   json.RawMessage `json:"json_value,omitempty"`   // internal: resolved json body
	RawJSON     json.RawMessage `json:"-"`                      // json body as provided (key: value)
	Fields      []FormField     `json:"fields,omitempty"`       // form
	Parts       []MultipartPart `json:"parts,omitempty"`        // multipart
	ArtifactRef string          `json:"artifact_ref,omitempty"` // artifact
}

// UnmarshalJSON enforces the tagged union with payload-key rules.
func (b *BodySpec) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("body: %w", err)
	}
	if trailingJSON(dec) {
		return fmt.Errorf("body: unexpected trailing data")
	}
	tRaw, ok := raw["type"]
	if !ok {
		return fmt.Errorf("body: type is required")
	}
	var t string
	if err := json.Unmarshal(tRaw, &t); err != nil {
		return fmt.Errorf("body.type: %w", err)
	}
	b.Type = BodyType(t)
	has := func(k string) bool { _, ok := raw[k]; return ok }

	switch b.Type {
	case BodyText, BodyBase64:
		for _, k := range []string{"fields", "parts", "artifact_ref"} {
			if has(k) {
				return fmt.Errorf("body type %q does not accept %q", b.Type, k)
			}
		}
		v, ok := raw["value"]
		if !ok {
			return fmt.Errorf("body type %q requires value", b.Type)
		}
		if err := json.Unmarshal(v, &b.Value); err != nil {
			return fmt.Errorf("body value: %w", err)
		}
	case BodyJSON:
		for _, k := range []string{"fields", "parts", "artifact_ref"} {
			if has(k) {
				return fmt.Errorf("body type %q does not accept %q", b.Type, k)
			}
		}
		v, ok := raw["value"]
		if !ok {
			return fmt.Errorf("body type json requires value")
		}
		if !json.Valid(v) {
			return fmt.Errorf("body json value is not valid JSON")
		}
		b.RawJSON = append(json.RawMessage(nil), v...)
	case BodyForm:
		for _, k := range []string{"value", "parts", "artifact_ref"} {
			if has(k) {
				return fmt.Errorf("body type form does not accept %q", k)
			}
		}
		f, ok := raw["fields"]
		if !ok {
			return fmt.Errorf("body type form requires fields")
		}
		if err := strictDecode(f, &b.Fields); err != nil {
			return fmt.Errorf("body fields: %w", err)
		}
	case BodyMultipart:
		for _, k := range []string{"value", "fields", "artifact_ref"} {
			if has(k) {
				return fmt.Errorf("body type multipart does not accept %q", k)
			}
		}
		p, ok := raw["parts"]
		if !ok {
			return fmt.Errorf("body type multipart requires parts")
		}
		if err := strictDecode(p, &b.Parts); err != nil {
			return fmt.Errorf("body parts: %w", err)
		}
	case BodyArtifact:
		for _, k := range []string{"value", "fields", "parts"} {
			if has(k) {
				return fmt.Errorf("body type artifact does not accept %q", k)
			}
		}
		v, ok := raw["artifact_ref"]
		if !ok {
			return fmt.Errorf("body type artifact requires artifact_ref")
		}
		if err := json.Unmarshal(v, &b.ArtifactRef); err != nil {
			return fmt.Errorf("body artifact_ref: %w", err)
		}
	default:
		return fmt.Errorf("body type must be one of text|json|base64|form|multipart|artifact, got %q", t)
	}
	return nil
}

// MarshalJSON canonicalizes the body spec for fingerprinting (secrets in
// inline values are replaced before this is ever logged — see SanitizePlan).
func (b BodySpec) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": b.Type}
	switch b.Type {
	case BodyText:
		m["value"] = b.Value
	case BodyBase64:
		m["value"] = b.Value
	case BodyJSON:
		if len(b.RawJSON) > 0 {
			m["value"] = b.RawJSON
		} else if len(b.JSONValue) > 0 {
			m["value"] = b.JSONValue
		}
	case BodyForm:
		m["fields"] = b.Fields
	case BodyMultipart:
		m["parts"] = b.Parts
	case BodyArtifact:
		m["artifact_ref"] = b.ArtifactRef
	}
	return json.Marshal(m)
}

// FormField is one urlencoded form pair.
type FormField struct {
	Name  string    `json:"name"`
	Value ValueExpr `json:"value"`
}

// MultipartPart is a text field or a scoped artifact file part.
type MultipartPart struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`        // text part
	ArtifactRef string `json:"artifact_ref,omitempty"` // file part
	Filename    string `json:"filename,omitempty"`     // display name override
}

// UnmarshalJSON enforces the text/artifact part union.
func (p *MultipartPart) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("multipart part: %w", err)
	}
	if trailingJSON(dec) {
		return fmt.Errorf("multipart part: unexpected trailing data")
	}
	allowed := map[string]bool{"name": true, "value": true, "artifact_ref": true, "filename": true}
	for k := range raw {
		if !allowed[k] {
			return fmt.Errorf("multipart part: unknown field %q", k)
		}
	}
	n, ok := raw["name"]
	if !ok {
		return fmt.Errorf("multipart part: name is required")
	}
	if err := json.Unmarshal(n, &p.Name); err != nil {
		return fmt.Errorf("multipart part name: %w", err)
	}
	_, hasValue := raw["value"]
	_, hasArtifact := raw["artifact_ref"]
	if hasValue == hasArtifact {
		return fmt.Errorf("multipart part requires exactly one of value|artifact_ref")
	}
	if hasValue {
		if err := json.Unmarshal(raw["value"], &p.Value); err != nil {
			return fmt.Errorf("multipart part value: %w", err)
		}
	}
	if hasArtifact {
		if err := json.Unmarshal(raw["artifact_ref"], &p.ArtifactRef); err != nil {
			return fmt.Errorf("multipart part artifact_ref: %w", err)
		}
	}
	if f, ok := raw["filename"]; ok {
		if err := json.Unmarshal(f, &p.Filename); err != nil {
			return fmt.Errorf("multipart part filename: %w", err)
		}
	}
	return nil
}

// ResponseSpec controls how the response body is consumed.
type ResponseSpec struct {
	Mode               ResponseMode `json:"mode,omitempty"`
	InlineMaxBytes     int64        `json:"inline_max_bytes,omitempty"`
	Filename           string       `json:"filename,omitempty"`
	IncludeRawArtifact bool         `json:"include_raw_artifact,omitempty"`
}

// ResponseMode selects inline preview, streamed download, or discard.
type ResponseMode string

const (
	// ResponseInline reads a bounded body preview.
	ResponseInline ResponseMode = "inline"
	// ResponseDownload streams the body into the artifact store.
	ResponseDownload ResponseMode = "download"
	// ResponseDiscard closes the body without application reads.
	ResponseDiscard ResponseMode = "discard"
)

func (m ResponseMode) valid() bool {
	return m == "" || m == ResponseInline || m == ResponseDownload || m == ResponseDiscard
}

// StatusMatch is a single status (from==to) or an inclusive range.
type StatusMatch struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// UnmarshalJSON accepts an integer or {"from":n,"to":m}.
func (s *StatusMatch) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9')) {
		var n int
		if err := json.Unmarshal(data, &n); err != nil {
			return fmt.Errorf("expect_status entry: %w", err)
		}
		s.From, s.To = n, n
		return nil
	}
	var r struct {
		From *int `json:"from"`
		To   *int `json:"to"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return fmt.Errorf("expect_status entry must be an integer or {from,to}: %w", err)
	}
	if r.From == nil || r.To == nil {
		return fmt.Errorf("expect_status range requires both from and to")
	}
	s.From, s.To = *r.From, *r.To
	return nil
}

// Matches reports whether the status falls inside the match.
func (s StatusMatch) Matches(status int) bool {
	return status >= s.From && status <= s.To
}

// ExtractSource selects where a variable's value comes from.
type ExtractSource string

const (
	// ExtractJSONPointer reads a value from the bounded JSON body.
	ExtractJSONPointer ExtractSource = "json_pointer"
	// ExtractHeader reads a response header value.
	ExtractHeader ExtractSource = "header"
	// ExtractStatus reads the response status code.
	ExtractStatus ExtractSource = "status"
	// ExtractArtifact references the step's download artifact.
	ExtractArtifact ExtractSource = "artifact"
)

// VarType is the declared type of an extracted variable; JSON types are
// preserved, not stringified.
type VarType string

const (
	// VarString declares a string variable.
	VarString VarType = "string"
	// VarNumber declares a float variable.
	VarNumber VarType = "number"
	// VarInteger declares an integer variable.
	VarInteger VarType = "integer"
	// VarBoolean declares a boolean variable.
	VarBoolean VarType = "boolean"
)

// ExtractSpec declares one variable binding for later steps.
type ExtractSpec struct {
	Name       string          `json:"name"`
	Source     ExtractSource   `json:"source"`
	Pointer    string          `json:"pointer,omitempty"`
	HeaderName string          `json:"header,omitempty"`
	HeaderIdx  int             `json:"index,omitempty"`
	Type       VarType         `json:"type,omitempty"`
	Default    json.RawMessage `json:"default,omitempty"`
}

// UnmarshalJSON enforces the closed field set per source.
func (e *ExtractSpec) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name    string          `json:"name"`
		Source  string          `json:"source"`
		Pointer *string         `json:"pointer"`
		Header  *string         `json:"header"`
		Index   *int            `json:"index"`
		Type    string          `json:"type"`
		Default json.RawMessage `json:"default"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if trailingJSON(dec) {
		return fmt.Errorf("extract: unexpected trailing data")
	}
	e.Name, e.Source = raw.Name, ExtractSource(raw.Source)
	e.Pointer, e.HeaderName, e.Type = deref(raw.Pointer), deref(raw.Header), VarType(raw.Type)
	if raw.Index != nil {
		e.HeaderIdx = *raw.Index
	}
	e.Default = append(json.RawMessage(nil), raw.Default...)
	switch e.Source {
	case ExtractJSONPointer:
		if raw.Pointer == nil {
			return fmt.Errorf("extract json_pointer requires pointer")
		}
		if raw.Header != nil {
			return fmt.Errorf("extract json_pointer does not accept header")
		}
	case ExtractHeader:
		if raw.Header == nil {
			return fmt.Errorf("extract header requires header name")
		}
		if raw.Pointer != nil {
			return fmt.Errorf("extract header does not accept pointer")
		}
	case ExtractStatus, ExtractArtifact:
		if raw.Pointer != nil || raw.Header != nil {
			return fmt.Errorf("extract %s accepts neither pointer nor header", e.Source)
		}
	default:
		return fmt.Errorf("extract source must be one of json_pointer|header|status|artifact, got %q", raw.Source)
	}
	return nil
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// strictDecode decodes JSON into v rejecting unknown fields.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// trailingJSON reports whether more data follows the first decoded value.
func trailingJSON(dec *json.Decoder) bool {
	_, err := dec.Token()
	return err != io.EOF
}

// ParsePlan decodes and structurally validates a model-provided plan.
// The Go-side check is authoritative: the LLM-facing JSON Schema is a
// description, not a security boundary.
func ParsePlan(data []byte) (Plan, error) {
	var p Plan
	if err := strictDecode(data, &p); err != nil {
		return p, fmt.Errorf("invalid plan: %w", err)
	}
	return p, nil
}
