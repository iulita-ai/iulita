// Package publictools bridges a small allowlist of public information skills
// into fenced mobile turns. It never exposes the general legacy skill registry.
package publictools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/datetime"
	"github.com/iulita-ai/iulita/internal/skill/weather"
	"github.com/iulita-ai/iulita/internal/skill/webfetch"
	"github.com/iulita-ai/iulita/internal/skill/websearch"
)

// Lookup must apply current enablement and capability checks on every lookup.
type Lookup interface {
	Get(string) (skill.Skill, bool)
}

// Tools reuses the configured HTTP/proxy/search clients of these built-ins.
// None reads legacy account memory, credentials or a phone observation. Weather
// requires an explicit location to avoid legacy memory/IP location resolution.
func Tools(registry Lookup) []engine.Tool {
	if registry == nil {
		return nil
	}
	var tools []engine.Tool
	for _, name := range []string{"datetime", "weather", "websearch", "webfetch"} {
		s, ok := registry.Get(name)
		if !ok || s.Name() != name || !builtin(s) {
			continue
		}
		revision := "public-information-v1"
		if bindings, ok := registry.(interface {
			NativeBinding(skill.Skill) (string, bool)
		}); ok {
			binding, valid := bindings.NativeBinding(s)
			if !valid {
				continue
			}
			revision += ":" + binding
		}
		schema := bytes.Clone(s.InputSchema())
		description := s.Description()
		if name == "weather" {
			schema = json.RawMessage(`{"type":"object","properties":{"location":{"type":"string","description":"City or coordinates explicitly supplied by the user. Ask the user if unknown; never use server IP or phone data without consent."},"days":{"type":"integer","minimum":1,"maximum":16,"description":"Forecast days starting today; use at least 2 for tomorrow."}},"required":["location"],"additionalProperties":false}`)
			description = "Get current weather and forecasts from online weather services for an explicitly supplied city or coordinates. If the location is unknown, ask the user which city."
		}
		tools = append(tools, engine.Tool{
			Definition: llm.ToolDefinition{Name: name, Description: description, InputSchema: schema},
			Revision:   revision, Effect: "read", Isolated: true,
			RequiredScopes: []string{"chats:write"},
			Execute: func(ctx context.Context, _ conversations.Fence, input json.RawMessage) (string, error) {
				current, enabled := registry.Get(name)
				if !enabled || current != s {
					return "", engine.Rejection{}
				}
				if len(input) > 8<<10 || !validInput(name, input) {
					return `{"status":"invalid_input","message":"Supply valid arguments. Weather needs a city or explicitly shared coordinates; ask the user if unknown."}`, nil
				}
				parent := ctx
				ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
				defer cancel()
				// Execute the captured built-in, not a later registry replacement.
				var out string
				var err error
				if name == "weather" {
					forecast, ok := s.(interface {
						Forecast(context.Context, string, int) (string, error)
					})
					if !ok {
						return "", engine.Rejection{}
					}
					var v struct {
						Location string `json:"location"`
						Days     int    `json:"days"`
					}
					if json.Unmarshal(input, &v) != nil {
						return "", engine.Rejection{}
					}
					out, err = forecast.Forecast(ctx, v.Location, v.Days)
				} else {
					out, err = s.Execute(ctx, input)
				}
				if err != nil {
					if parent.Err() != nil {
						return "", parent.Err()
					}
					// Network errors can contain credentials/URLs. This public read
					// failed; it has no ambiguous external mutation to replay.
					return `{"status":"lookup_failed","message":"This public information lookup failed. Do not invent results or infer that the phone is offline."}`, nil
				}
				if len(out) > 48<<10 {
					out = out[:48<<10]
					for !utf8.ValidString(out) {
						out = out[:len(out)-1]
					}
					out += "\n[truncated]"
				}
				return out, nil
			},
		})
	}
	return tools
}

func builtin(s skill.Skill) bool {
	switch s.(type) {
	case *datetime.Skill, *weather.Skill, *websearch.Skill, *webfetch.Skill:
		return true
	}
	return false
}

func validInput(name string, input json.RawMessage) bool {
	// Reject null, unknown fields and invalid types before entering legacy code.
	switch name {
	case "weather":
		var v struct {
			Location string `json:"location"`
			Days     int    `json:"days"`
		}
		return decode(input, &v) && strings.TrimSpace(v.Location) != "" && len(v.Location) <= 500 && v.Days >= 0 && v.Days <= 16
	case "websearch":
		var v struct {
			Query string `json:"query"`
			Count int    `json:"count"`
		}
		return decode(input, &v) && strings.TrimSpace(v.Query) != "" && len(v.Query) <= 2000 && v.Count >= 0 && v.Count <= 10
	case "webfetch":
		var v struct {
			URL string `json:"url"`
		}
		return decode(input, &v) && strings.TrimSpace(v.URL) != "" && len(v.URL) <= 4000
	case "datetime":
		var v struct {
			Timezone string `json:"timezone"`
		}
		return decode(input, &v) && len(v.Timezone) <= 100
	}
	return false
}

func decode(input json.RawMessage, value any) bool {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(input) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(input))
	d.DisallowUnknownFields()
	return d.Decode(value) == nil
}
