package datetime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
)

// SynthesisRouteHint implements skill.SynthesisModelDeclarer.
func (s *Skill) SynthesisRouteHint() string { return llm.RouteHintCheap }

// Skill returns the current date, time, timezone, and unix timestamp.
type Skill struct{}

func New() *Skill { return &Skill{} }

func (s *Skill) Name() string        { return "datetime" }
func (s *Skill) Description() string { return "Get the current date, time, and timezone." }

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"timezone": {
				"type": "string",
				"description": "IANA timezone, e.g. Europe/Helsinki. Defaults to UTC."
			}
		}
	}`)
}

type input struct {
	Timezone string `json:"timezone"`
}

func (s *Skill) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var in input
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	}

	loc := time.UTC
	if in.Timezone != "" {
		var err error
		loc, err = time.LoadLocation(in.Timezone)
		if err != nil {
			return "", fmt.Errorf("unknown timezone %q: %w", in.Timezone, err)
		}
	}

	now := time.Now().In(loc)
	return fmt.Sprintf("Date: %s\nTime: %s\nTimezone: %s\nUnix: %d",
		now.Format("2006-01-02"),
		now.Format("15:04:05"),
		loc.String(),
		now.Unix(),
	), nil
}
