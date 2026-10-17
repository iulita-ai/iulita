package httpfetch

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/iulita-ai/iulita/internal/security"
)

// mutationPreview describes only the current, already resolved step. A plan-wide
// grant cannot safely approve destinations and inputs obtained from later I/O.
// This is a projection: it never resolves credential references or exposes
// extracted sensitive values. Oversized intentions fail closed, never truncate.
func mutationPreview(step Request, target *url.URL, sensitiveQuery map[int]bool, state *runState) (string, error) {
	displayURL := sanitizedURLString(target, sensitiveQuery)
	if display, err := url.Parse(displayURL); err == nil {
		// Preserve query order, duplicate keys and unusual separators exactly.
		// url.Values would silently discard semicolon-containing parameters.
		parts := strings.Split(display.RawQuery, "&")
		for index, part := range parts {
			key, _, hasValue := strings.Cut(part, "=")
			decoded, decodeErr := url.QueryUnescape(key)
			if decodeErr != nil {
				return "", fmt.Errorf("cannot safely describe HTTP query")
			}
			if hasValue && previewSensitiveName(decoded) {
				parts[index] = key + "=[redacted]"
			}
		}
		display.RawQuery = strings.Join(parts, "&")
		displayURL = display.String()
	}
	// Only mask sensitive variables actually used as path segments. Never
	// replace unrelated extracted values across a resolved origin/hostname.
	if display, err := url.Parse(displayURL); err == nil {
		for _, segment := range step.PathSegments {
			value, exists := state.vars[segment.Ref]
			if segment.Kind != ValueVar || !exists || !value.Sensitive {
				continue
			}
			if text, err := scalarToString(value.Value, "preview"); err == nil && text != "" {
				display.Path = strings.ReplaceAll(display.Path, text, "[redacted]")
				display.RawPath = ""
			}
		}
		displayURL = display.String()
	}
	if previewText(displayURL) != displayURL {
		return "", fmt.Errorf("HTTP target contains unreviewable sensitive data; use a credential reference")
	}
	intent := map[string]any{"method": normalizeMethod(step.Method), "url": displayURL, "redirects": step.Redirects}
	if step.Host != "" {
		intent["host"] = previewText(step.Host)
	}
	if step.Auth != nil {
		intent["authentication"] = map[string]any{"type": step.Auth.Type, "material": "[redacted]"}
	}
	if len(step.Headers) > 0 {
		headers := make([]any, 0, len(step.Headers))
		for _, header := range step.Headers {
			values := make([]any, 0, len(header.Values))
			for _, value := range header.Values {
				if previewSensitiveName(header.Name) {
					values = append(values, "[redacted]")
				} else {
					values = append(values, previewValue(value, state))
				}
			}
			headers = append(headers, map[string]any{"name": header.Name, "values": values})
		}
		intent["headers"] = headers
	}
	if step.Body != nil {
		b := step.Body
		body := map[string]any{"type": b.Type}
		switch b.Type {
		case BodyJSON:
			var value any
			decoder := json.NewDecoder(bytes.NewReader(b.RawJSON))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return "", fmt.Errorf("cannot describe JSON mutation")
			}
			body["value"] = previewJSON(value, state, true)
		case BodyText:
			if previewText(b.Value) != b.Value {
				return "", fmt.Errorf("HTTP text body cannot be reviewed safely; use structured JSON or form fields")
			}
			body["value"] = b.Value
		case BodyBase64:
			data, err := base64.StdEncoding.DecodeString(b.Value)
			if err != nil {
				return "", fmt.Errorf("cannot describe binary mutation")
			}
			body["bytes"] = len(data)
			body["sha256"] = fmt.Sprintf("%x", sha256.Sum256(data))
		case BodyForm:
			fields := make([]any, 0, len(b.Fields))
			for _, field := range b.Fields {
				value := previewValue(field.Value, state)
				if previewSensitiveName(field.Name) {
					value = "[redacted]"
				}
				fields = append(fields, map[string]any{"name": field.Name, "value": value})
			}
			body["fields"] = fields
		case BodyArtifact:
			body["artifact_ref"] = previewText(b.ArtifactRef)
		case BodyMultipart:
			parts := make([]any, 0, len(b.Parts))
			for _, part := range b.Parts {
				value := map[string]any{"name": part.Name}
				switch {
				case part.ArtifactRef != "":
					value["artifact_ref"] = previewText(part.ArtifactRef)
					value["filename"] = previewText(part.Filename)
				case previewSensitiveName(part.Name):
					value["value"] = "[redacted]"
				default:
					value["value"] = previewText(part.Value)
				}
				parts = append(parts, value)
			}
			body["parts"] = parts
		}
		intent["body"] = body
	}
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return "", fmt.Errorf("cannot describe HTTP mutation")
	}
	const prefix = "Approve this resolved HTTP operation (sensitive fields are redacted):\n"
	if len(data)+len(prefix) > 3900 {
		return "", fmt.Errorf("HTTP operation is too large to review safely; submit a smaller request")
	}
	return prefix + string(data), nil
}

func previewValue(value ValueExpr, state *runState) any {
	switch value.Kind {
	case ValueCredential:
		return "[redacted credential]"
	case ValueVar:
		variable, ok := state.vars[value.Ref]
		if !ok || variable.Sensitive {
			return "[redacted variable]"
		}
		return previewJSON(variable.Value, state, false)
	default:
		return previewText(value.Literal)
	}
}

func previewJSON(value any, state *runState, refs bool) any {
	switch v := value.(type) {
	case map[string]any:
		if refs && len(v) == 1 {
			if _, ok := v["credential_ref"]; ok {
				return "[redacted credential]"
			}
			if name, ok := v["$var"].(string); ok {
				return previewValue(ValueExpr{Kind: ValueVar, Ref: name}, state)
			}
			if literal, ok := v["$literal"]; ok {
				return previewJSON(literal, state, false)
			}
		}
		out := make(map[string]any, len(v))
		for key, child := range v {
			if previewSensitiveName(key) {
				out[key] = "[redacted]"
			} else {
				out[key] = previewJSON(child, state, refs)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for index, child := range v {
			out[index] = previewJSON(child, state, refs)
		}
		return out
	case string:
		return previewText(v)
	default:
		return v
	}
}
func previewText(value string) string {
	check := strings.ReplaceAll(value, "[redacted]", "")
	check = strings.ReplaceAll(check, url.QueryEscape("[redacted]"), "")
	if found, _ := security.Contains(check); found {
		return "[redacted sensitive text]"
	}
	return value
}
func previewSensitiveName(name string) bool {
	// Camel-case auth fields are common in JSON APIs (accessToken/clientSecret).
	var normalized strings.Builder
	for index, char := range name {
		if index > 0 && char >= 'A' && char <= 'Z' && name[index-1] >= 'a' && name[index-1] <= 'z' {
			normalized.WriteByte('_')
		}
		normalized.WriteRune(char)
	}
	lower := strings.ToLower(normalized.String())
	if sensitiveResponseHeaders[lower] || lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" {
		return true
	}
	lower = strings.NewReplacer("-", "_", ".", "_").Replace(lower)
	for _, token := range sensitiveQueryNames {
		if lower == token || strings.HasPrefix(lower, token+"_") || strings.HasSuffix(lower, "_"+token) {
			return true
		}
	}
	return false
}
