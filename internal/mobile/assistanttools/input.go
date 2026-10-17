package assistanttools

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// Reviewed account schemas are flat objects. Reject unknown/duplicate identity
// fields and unsupported types BEFORE StartTool, never after a possible effect.
func validShape(schema, input json.RawMessage) bool {
	if len(input) > 32<<10 || !utf8.Valid(input) {
		return false
	}
	var shape struct {
		Properties map[string]struct {
			Type    string
			Enum    []string
			Minimum *float64
			Items   struct{ Type string }
		}
		Required []string
	}
	if json.Unmarshal(schema, &shape) != nil {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		property, ok := shape.Properties[key]
		if !ok {
			return false
		}
		var value any
		if d.Decode(&value) != nil {
			return false
		}
		switch property.Type {
		case "string":
			v, ok := value.(string)
			if !ok || len(v) > 16<<10 {
				return false
			}
			if len(property.Enum) > 0 {
				found := false
				for _, allowed := range property.Enum {
					if v == allowed {
						found = true
						break
					}
				}
				if !found {
					return false
				}
			}
		case "integer":
			v, ok := value.(json.Number)
			if !ok {
				return false
			}
			n, parseErr := strconv.ParseInt(string(v), 10, 64)
			if parseErr != nil || n < 0 || n > 1<<53 {
				return false
			}
			if property.Minimum != nil && float64(n) < *property.Minimum {
				return false
			}
		case "number":
			v, ok := value.(json.Number)
			if !ok {
				return false
			}
			if _, parseErr := strconv.ParseFloat(string(v), 64); parseErr != nil {
				return false
			}
		case "array":
			items, ok := value.([]any)
			if !ok || len(items) > 100 || property.Items.Type != "string" {
				return false
			}
			for _, item := range items {
				v, ok := item.(string)
				if !ok || len(v) > 1024 {
					return false
				}
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return false
			}
		default:
			return false
		}
	}
	if _, err = d.Token(); err != nil {
		return false
	}
	if d.More() {
		return false
	}
	for _, key := range shape.Required {
		if !seen[key] {
			return false
		}
	}
	return json.Valid(input)
}
