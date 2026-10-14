package httpfetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// runExtract applies the step's extract declarations after the response is
// available. Extracted values default to sensitive + origin-bound; the input
// cannot lower sensitivity (plan §5). JSON extraction is rejected on
// truncated bodies with an explicit error unless the variable has a default.
func (e *Engine) runExtract(step Request, resp *http.Response, sr StepResult, entity []byte, bodyComplete bool, state *runState, origin Origin) error {
	for _, ex := range step.Extract {
		vv, err := e.extractOne(ex, resp, sr, entity, bodyComplete, origin)
		if err != nil {
			// Missing values fall back to the declared default when present.
			if len(ex.Default) > 0 {
				dv, derr := defaultValue(ex)
				if derr != nil {
					return fmt.Errorf("extract %s: %w", ex.Name, derr)
				}
				vv = dv
			} else {
				return fmt.Errorf("extract %s: %w", ex.Name, err)
			}
		}
		if vv.Origin.Equal(Origin{}) {
			vv.Origin = origin
		}
		vv.Sensitive = true // server policy may mark specific fields public later
		vv.Declared = ex.Type
		state.vars[ex.Name] = vv
		sr.Vars = append(sr.Vars, VarView{Name: ex.Name, Type: declaredType(vv.Value, ex.Type), Sensitive: true})
	}
	return nil
}

func declaredType(v any, declared VarType) VarType {
	if declared != "" {
		return declared
	}
	switch v.(type) {
	case string:
		return VarString
	case bool:
		return VarBoolean
	case int64:
		return VarInteger
	case json.Number, float64:
		return VarNumber
	}
	return ""
}

// extractOne computes one variable value.
func (e *Engine) extractOne(ex ExtractSpec, resp *http.Response, sr StepResult, entity []byte, bodyComplete bool, origin Origin) (varValue, error) {
	switch ex.Source {
	case ExtractStatus:
		return varValue{Value: int64(resp.StatusCode), Origin: origin}, nil
	case ExtractHeader:
		values := lookupHeader(resp.Header, ex.HeaderName)
		if ex.HeaderIdx >= len(values) {
			return varValue{}, fmt.Errorf("header %q has no value at index %d", ex.HeaderName, ex.HeaderIdx)
		}
		v, err := coerceStringTo(ex.Type, values[ex.HeaderIdx])
		if err != nil {
			return varValue{}, err
		}
		return varValue{Value: v, Origin: origin}, nil
	case ExtractArtifact:
		if sr.Artifact == nil {
			return varValue{}, fmt.Errorf("step produced no artifact")
		}
		return varValue{Value: sr.Artifact.ID, Origin: origin}, nil
	case ExtractJSONPointer:
		if len(entity) == 0 {
			return varValue{}, fmt.Errorf("no inline body available for extraction")
		}
		if !bodyComplete {
			return varValue{}, fmt.Errorf("incomplete_body: JSON extraction requires a complete body")
		}
		return extractPointer(ex, entity)
	}
	return varValue{}, fmt.Errorf("unsupported extract source %q", ex.Source)
}

// extractPointer evaluates a JSON Pointer against the full inline entity.
func extractPointer(ex ExtractSpec, entity []byte) (varValue, error) {
	var root any
	dec := json.NewDecoder(bytes.NewReader(entity))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return varValue{}, fmt.Errorf("body is not valid JSON: %w", err)
	}
	cur := root
	for _, tok := range parseJSONPointer(ex.Pointer) {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[tok]
			if !ok {
				return varValue{}, fmt.Errorf("pointer %s: key %q not found", ex.Pointer, tok)
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return varValue{}, fmt.Errorf("pointer %s: invalid array index %q", ex.Pointer, tok)
			}
			cur = node[idx]
		default:
			return varValue{}, fmt.Errorf("pointer %s: cannot descend into scalar at %q", ex.Pointer, tok)
		}
	}
	v, err := coerceType(ex.Type, cur)
	if err != nil {
		return varValue{}, err
	}
	return varValue{Value: v}, nil
}

// coerceStringTo converts a header string to the declared type.
func coerceStringTo(declared VarType, raw string) (any, error) {
	switch declared {
	case "", VarString:
		return raw, nil
	case VarInteger:
		i, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("header value is not an integer")
		}
		return i, nil
	case VarNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, fmt.Errorf("header value is not a number")
		}
		return f, nil
	case VarBoolean:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("header value is not a boolean")
		}
		return b, nil
	}
	return raw, nil
}

// defaultValue type-checks the declared fallback.
func defaultValue(ex ExtractSpec) (varValue, error) {
	var raw any
	dec := json.NewDecoder(bytes.NewReader(ex.Default))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return varValue{}, fmt.Errorf("invalid default: %w", err)
	}
	v, err := coerceType(ex.Type, raw)
	if err != nil {
		return varValue{}, err
	}
	return varValue{Value: v}, nil
}

// coerceType enforces the declared variable type, preserving JSON types
// instead of stringifying everything.
func coerceType(declared VarType, v any) (any, error) {
	if declared == "" {
		return v, nil
	}
	switch declared {
	case VarString:
		switch t := v.(type) {
		case string:
			return t, nil
		case json.Number:
			return t.String(), nil
		case bool:
			return strconv.FormatBool(t), nil
		}
		return nil, fmt.Errorf("value is not a string")
	case VarInteger:
		switch t := v.(type) {
		case json.Number:
			i, err := t.Int64()
			if err != nil {
				return nil, fmt.Errorf("value is not an integer")
			}
			return i, nil
		case int64:
			return t, nil
		case int:
			return int64(t), nil
		case string:
			i, err := strconv.ParseInt(t, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("value is not an integer")
			}
			return i, nil
		}
		return nil, fmt.Errorf("value is not an integer")
	case VarNumber:
		switch t := v.(type) {
		case json.Number:
			f, err := t.Float64()
			if err != nil {
				return nil, fmt.Errorf("value is not a number")
			}
			return f, nil
		case float64:
			return t, nil
		case int64:
			return float64(t), nil
		case string:
			s := unquoteTrim(t)
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("value is not a number")
			}
			return f, nil
		}
		return nil, fmt.Errorf("value is not a number")
	case VarBoolean:
		switch t := v.(type) {
		case bool:
			return t, nil
		case string:
			b, err := strconv.ParseBool(unquoteTrim(t))
			if err != nil {
				return nil, fmt.Errorf("value is not a boolean")
			}
			return b, nil
		}
		return nil, fmt.Errorf("value is not a boolean")
	}
	return v, nil
}

// parseJSONPointer splits a pointer into unescaped tokens.
func parseJSONPointer(pointer string) []string {
	if pointer == "" || pointer == "/" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, p := range parts {
		p = strings.ReplaceAll(p, "~1", "/")
		parts[i] = strings.ReplaceAll(p, "~0", "~")
	}
	return parts
}

// unquoteTrim strips JSON-style quotes produced by Number(strconv.Quote(...)).
func unquoteTrim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if err := json.Unmarshal([]byte(s), &out); err == nil {
			return out
		}
	}
	return s
}
