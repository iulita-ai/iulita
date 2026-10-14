package httpfetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// resolveValue resolves a ValueExpr to its final string form. Credential refs
// go through the narrow resolver (origin/caller bound); variables keep their
// declared sensitivity. Only scalar values are accepted here — objects and
// arrays are for JSON body positions only.
func (e *Engine) resolveValue(ctx context.Context, state *runState, scope Scope, origin Origin, v ValueExpr) (value string, sensitive bool, err error) {
	switch v.Kind {
	case ValueLiteral:
		return v.Literal, false, nil
	case ValueVar:
		val, ok := state.vars[v.Ref]
		if !ok {
			return "", false, fmt.Errorf("unknown variable %q", v.Ref)
		}
		s, err := scalarToString(val.Value, v.Ref)
		if err != nil {
			return "", false, err
		}
		return s, val.Sensitive, nil
	case ValueCredential:
		if e.creds == nil {
			return "", true, fmt.Errorf("credential %q requested but no credential resolver is configured", v.Ref)
		}
		cred, err := e.creds.ResolveHTTP(ctx, scope, v.Ref, origin, CredUsageHeader)
		if err != nil {
			return "", true, fmt.Errorf("credential %q: %w", v.Ref, err)
		}
		switch {
		case cred.Secret != "":
			return cred.Secret, true, nil
		case cred.Token != "":
			return cred.Token, true, nil
		default:
			return "", true, fmt.Errorf("credential %q resolved to an unusable value for this position", v.Ref)
		}
	}
	return "", false, fmt.Errorf("unsupported value")
}

// scalarToString renders scalar variables; composite values are rejected.
func scalarToString(v any, name string) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case json.Number:
		return t.String(), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	}
	return "", fmt.Errorf("variable %q is not a scalar and cannot be used in this position", name)
}

// buildRequest assembles the http.Request for one hop: resolved headers,
// auth, cookies, and the typed body with engine-managed Content-Type.
// Returns the body bytes (for redirect replay and fingerprinting).
func (e *Engine) buildRequest(ctx context.Context, step Request, state *runState, scope Scope, method, target string) (req *http.Request, bodyBytes []byte, err error) {
	origin := mustOrigin(target)

	// Body first: content rules depend on the computed size.
	var contentType string
	bodyBytes, contentType, err = e.buildBody(ctx, step, state, scope, origin)
	if err != nil {
		return nil, nil, err
	}

	var bodyReader io.Reader
	if bodyBytes != nil {
		bodyReader = bytes.NewReader(bodyBytes)
	}
	req, err = http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("building request: %w", err)
	}
	if step.Host != "" {
		req.Host = step.Host
	}

	declaredCT := ""
	declaredCL := int64(-1)
	hasAuth := step.Auth != nil && step.Auth.Type != AuthNone && step.Auth.Type != ""
	for _, h := range step.Headers {
		lower := strings.ToLower(h.Name)
		values := make([]string, 0, len(h.Values))
		for _, ve := range h.Values {
			val, _, rerr := e.resolveValue(ctx, state, scope, origin, ve)
			if rerr != nil {
				return nil, nil, fmt.Errorf("header %s: %w", h.Name, rerr)
			}
			val = strings.TrimSpace(val)
			if err := validateHeaderValue(h.Name, val); err != nil {
				return nil, nil, err
			}
			values = append(values, val)
		}
		switch lower {
		case "content-type":
			// Conflict with engine-managed body encodings is rejected (v1).
			switch bodyType(step) {
			case BodyJSON, BodyForm, BodyMultipart:
				return nil, nil, fmt.Errorf("content-type header conflicts with body type %q (engine sets it)", bodyType(step))
			}
			declaredCT = strings.Join(values, ", ")
		case "content-length":
			if len(values) != 1 {
				return nil, nil, fmt.Errorf("content-length must have exactly one value")
			}
			n, perr := strconv.ParseInt(values[0], 10, 64)
			if perr != nil {
				return nil, nil, fmt.Errorf("content-length %q is not a number", values[0])
			}
			declaredCL = n
			continue // transport sets the real length
		}
		req.Header[http.CanonicalHeaderKey(h.Name)] = values
	}

	// Content-Type: engine default when the body needs one and none declared.
	if bodyBytes != nil && declaredCT == "" && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Auth application.
	if hasAuth {
		if err := e.applyAuth(ctx, req, step.Auth, scope, origin); err != nil {
			return nil, nil, err
		}
	}

	// Chain cookies: exact-origin provenance only.
	if state.jar != nil {
		if cookies := state.jar.cookiesFor(origin, req.URL); len(cookies) > 0 {
			parts := make([]string, 0, len(cookies))
			for _, c := range cookies {
				parts = append(parts, c.name+"="+c.value)
			}
			req.Header.Set("Cookie", strings.Join(parts, "; "))
		}
	}

	// Content-Length exact-match rule.
	if declaredCL >= 0 {
		if bodyBytes == nil || int64(len(bodyBytes)) != declaredCL {
			return nil, nil, fmt.Errorf("declared content-length %d does not match the actual body size", declaredCL)
		}
	}

	if state.jar != nil {
		state.jar.recordRequestSent(origin)
	}
	return req, bodyBytes, nil
}

func bodyType(step Request) BodyType {
	if step.Body == nil {
		return ""
	}
	return step.Body.Type
}

// applyAuth sets Authorization from inline data or a resolved credential.
func (e *Engine) applyAuth(ctx context.Context, req *http.Request, auth *AuthSpec, scope Scope, origin Origin) error {
	switch auth.Type {
	case AuthNone, "":
		return nil
	case AuthBasic:
		if auth.CredentialRef != "" {
			if e.creds == nil {
				return fmt.Errorf("credential %q requested but no credential resolver is configured", auth.CredentialRef)
			}
			cred, err := e.creds.ResolveHTTP(ctx, scope, auth.CredentialRef, origin, CredUsageBasic)
			if err != nil {
				return fmt.Errorf("credential %q: %w", auth.CredentialRef, err)
			}
			if cred.Username == "" && cred.Password == "" {
				return fmt.Errorf("credential %q is not a basic credential", auth.CredentialRef)
			}
			req.SetBasicAuth(cred.Username, cred.Password)
			return nil
		}
		req.SetBasicAuth(auth.Username, auth.Password)
		return nil
	case AuthBearer:
		if auth.CredentialRef != "" {
			if e.creds == nil {
				return fmt.Errorf("credential %q requested but no credential resolver is configured", auth.CredentialRef)
			}
			cred, err := e.creds.ResolveHTTP(ctx, scope, auth.CredentialRef, origin, CredUsageBearer)
			if err != nil {
				return fmt.Errorf("credential %q: %w", auth.CredentialRef, err)
			}
			token := cred.Token
			if token == "" {
				token = cred.Secret
			}
			if token == "" {
				return fmt.Errorf("credential %q has no bearer token", auth.CredentialRef)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+auth.Token)
		return nil
	}
	return fmt.Errorf("unsupported auth type %q", auth.Type)
}

// buildBody renders the typed body and its engine-managed content type.
// A nil body yields nil bytes.
func (e *Engine) buildBody(ctx context.Context, step Request, state *runState, scope Scope, origin Origin) (body []byte, contentType string, err error) {
	b := step.Body
	if b == nil {
		return nil, "", nil
	}
	switch b.Type {
	case BodyText:
		if int64(len(b.Value)) > e.limits.MaxUploadBytes {
			return nil, "", fmt.Errorf("text body exceeds upload cap")
		}
		return []byte(b.Value), "text/plain; charset=utf-8", nil
	case BodyBase64:
		raw, err := base64.StdEncoding.DecodeString(b.Value)
		if err != nil {
			return nil, "", fmt.Errorf("invalid base64 body: %w", err)
		}
		if int64(len(raw)) > e.limits.MaxUploadBytes {
			return nil, "", fmt.Errorf("binary body exceeds upload cap")
		}
		return raw, "application/octet-stream", nil
	case BodyJSON:
		resolved, err := e.resolveJSONRefs(ctx, b.RawJSON, state, scope, origin)
		if err != nil {
			return nil, "", err
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		if err := enc.Encode(resolved); err != nil {
			return nil, "", fmt.Errorf("encoding json body: %w", err)
		}
		out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		if int64(len(out)) > e.limits.MaxUploadBytes {
			return nil, "", fmt.Errorf("json body exceeds upload cap")
		}
		return out, "application/json", nil
	case BodyForm:
		var buf bytes.Buffer
		for _, f := range b.Fields {
			val, _, err := e.resolveValue(ctx, state, scope, origin, f.Value)
			if err != nil {
				return nil, "", fmt.Errorf("form field %s: %w", f.Name, err)
			}
			if buf.Len() > 0 {
				buf.WriteByte('&')
			}
			buf.WriteString(urlQueryEscape(f.Name))
			buf.WriteByte('=')
			buf.WriteString(urlQueryEscape(val))
		}
		if int64(buf.Len()) > e.limits.MaxUploadBytes {
			return nil, "", fmt.Errorf("form body exceeds upload cap")
		}
		return buf.Bytes(), "application/x-www-form-urlencoded", nil
	case BodyMultipart:
		return e.buildMultipart(ctx, b, scope)
	case BodyArtifact:
		if e.artifacts == nil {
			return nil, "", fmt.Errorf("artifact uploads require an artifact store (unavailable)")
		}
		data, mime, err := e.readArtifactForUpload(ctx, scope, b.ArtifactRef)
		if err != nil {
			return nil, "", err
		}
		return data, mime, nil
	}
	return nil, "", fmt.Errorf("unsupported body type %q", b.Type)
}

// buildMultipart renders text and artifact parts with a generated boundary.
func (e *Engine) buildMultipart(ctx context.Context, b *BodySpec, scope Scope) (payload []byte, contentType string, err error) {
	if e.artifacts == nil {
		// Text-only multipart works without a store; artifact parts do not.
		for _, p := range b.Parts {
			if p.ArtifactRef != "" {
				return nil, "", fmt.Errorf("artifact parts require an artifact store (unavailable)")
			}
		}
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	total := int64(0)
	for _, p := range b.Parts {
		if p.ArtifactRef != "" {
			data, _, err := e.readArtifactForUpload(ctx, scope, p.ArtifactRef)
			if err != nil {
				return nil, "", fmt.Errorf("part %s: %w", p.Name, err)
			}
			fw, err := w.CreateFormFile(p.Name, sanitizeFilename(p.Filename, p.ArtifactRef))
			if err != nil {
				return nil, "", err
			}
			if _, err := fw.Write(data); err != nil {
				return nil, "", err
			}
			total += int64(len(data))
			continue
		}
		fw, err := w.CreateFormField(p.Name)
		if err != nil {
			return nil, "", err
		}
		if _, err := fw.Write([]byte(p.Value)); err != nil {
			return nil, "", err
		}
		total += int64(len(p.Value))
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	if total > e.limits.MaxUploadBytes || int64(buf.Len()) > e.limits.MaxUploadBytes*2 {
		return nil, "", fmt.Errorf("multipart body exceeds upload cap")
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// resolveJSONRefs walks the raw JSON body replacing {"$var"}/{"credential_ref"}
// objects with their typed values; {"$literal":…} escapes evaluation.
func (e *Engine) resolveJSONRefs(ctx context.Context, raw json.RawMessage, state *runState, scope Scope, origin Origin) (resolved any, err error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var parsed any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("json body: %w", err)
	}
	return e.resolveJSONValue(ctx, parsed, state, scope, origin)
}

func (e *Engine) resolveJSONValue(ctx context.Context, v any, state *runState, scope Scope, origin Origin) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$var"].(string); ok && len(t) == 1 {
			val, ok := state.vars[ref]
			if !ok {
				return nil, fmt.Errorf("unknown variable %q", ref)
			}
			return val.Value, nil
		}
		if ref, ok := t["credential_ref"].(string); ok && len(t) == 1 {
			if e.creds == nil {
				return nil, fmt.Errorf("credential %q requested but no credential resolver is configured", ref)
			}
			cred, err := e.creds.ResolveHTTP(ctx, scope, ref, origin, CredUsageHeader)
			if err != nil {
				return nil, fmt.Errorf("credential %q: %w", ref, err)
			}
			switch {
			case cred.Secret != "":
				return cred.Secret, nil
			case cred.Token != "":
				return cred.Token, nil
			default:
				return nil, fmt.Errorf("credential %q resolved to an unusable value", ref)
			}
		}
		if lit, ok := t["$literal"]; ok && len(t) == 1 {
			return lit, nil
		}
		out := make(map[string]any, len(t))
		for k, vv := range t {
			rv, err := e.resolveJSONValue(ctx, vv, state, scope, origin)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			rv, err := e.resolveJSONValue(ctx, vv, state, scope, origin)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	}
	return v, nil
}

// readArtifactForUpload opens a scoped artifact for upload with the cap.
func (e *Engine) readArtifactForUpload(ctx context.Context, scope Scope, ref string) (data []byte, mime string, err error) {
	if scope.UserID == "" {
		return nil, "", fmt.Errorf("artifact upload requires an authenticated owner")
	}
	r, err := e.artifacts.Open(ctx, scope.UserID, ref)
	if err != nil {
		return nil, "", fmt.Errorf("open artifact: %w", err)
	}
	defer r.Close() //nolint:errcheck
	limited := io.LimitReader(r, e.limits.MaxUploadBytes+1)
	data, err = io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("read artifact: %w", err)
	}
	if int64(len(data)) > e.limits.MaxUploadBytes {
		return nil, "", fmt.Errorf("artifact exceeds upload cap")
	}
	return data, "application/octet-stream", nil
}

// sanitizeFilename strips path separators and control characters.
func sanitizeFilename(name, fallback string) string {
	if name == "" {
		name = fallback
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == '/' || r == '\\' || r == ':' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 200 {
		out = out[:200]
	}
	if out == "" {
		return "file"
	}
	return out
}

func mustOrigin(rawURL string) Origin {
	if u, err := parseURL(rawURL); err == nil {
		return ParseOrigin(u)
	}
	return Origin{}
}
