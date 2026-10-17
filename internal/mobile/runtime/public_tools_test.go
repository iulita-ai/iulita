package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/defaults"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/publictools"
	"github.com/iulita-ai/iulita/internal/models"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/datetime"
	"github.com/iulita-ai/iulita/internal/skill/weather"
)

type publicProbeModel struct {
	rounds   atomic.Int32
	timezone string
}

func (p *publicProbeModel) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	p.rounds.Add(1)
	names := map[string]bool{}
	for _, tool := range r.Tools {
		names[tool.Name] = true
	}
	if !names["datetime"] || !names["weather"] || names["phone_action"] || names["memory_save"] || r.CacheScope != "" || !strings.Contains(r.SystemPrompt, "complete available tool set") || !strings.Contains(r.SystemPrompt, defaults.LocationPrompt) {
		return llm.Response{}, errors.New("public tool wiring or isolated policy invalid")
	}
	if len(r.ToolExchanges) == 0 {
		input, err := json.Marshal(map[string]string{"timezone": p.timezone})
		if err != nil {
			return llm.Response{}, err
		}
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "now", Name: "datetime", Input: input}}}, nil
	}
	result := r.ToolExchanges[0].Results
	if len(result) != 1 || !strings.Contains(result[0].Content, "Timezone: "+defaults.TimezoneOrDefault(p.timezone)) {
		return llm.Response{}, errors.New("public tool not executed")
	}
	return llm.Response{Content: "verified public tool response"}, nil
}

func TestPublicToolsExecuteInIsolatedChatWithDeviceFeatureDisabled(t *testing.T) {
	for _, timezone := range []string{"", "UTC"} {
		t.Run("timezone="+timezone, func(t *testing.T) {
			s, db, _, _ := runtimeFixture(t)
			if err := Initialize(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			p := &publicProbeModel{timezone: timezone}
			settings := models.Settings{SchemaVersion: 1, Profiles: models.CandidateProfiles(), Policy: models.Policy{Everyday: "ds-flash"}}
			bindings := map[string]llm.ProfileBinding{}
			for _, profile := range settings.Profiles {
				bindings[profile.ID] = llm.ProfileBinding{Profile: profile, Provider: p, Eligibility: "production_eligible"}
			}
			snapshot, err := llm.NewProfileSnapshot(1, settings, bindings)
			if err != nil {
				t.Fatal(err)
			}
			registry := skill.NewRegistry()
			registry.Register(datetime.New())
			registry.Register(weather.New(nil, nil, nil)) // No account/IP location lookup allowed.
			r, err := NewWithTools(context.Background(), s, config.MobileConfig{ChatEnabled: true}, browseroauth.PasswordAccounts{Users: db}, fixtureInvoker{snapshot}, "system", "test", publictools.Tools(registry))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			users, err := db.ListUsers(context.Background())
			if err != nil || len(users) != 1 {
				t.Fatal("fixture owner", err)
			}
			owner := users[0].ID
			chat, err := r.chats.Create(context.Background(), owner, "tools", conversations.Settings{ProfileID: "ds-flash", MemoryMode: "isolated"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- r.engine.Run(ctx) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("worker did not stop")
				}
			}()
			select {
			case <-r.engine.Ready():
			case <-time.After(3 * time.Second):
				t.Fatal("worker not ready")
			}
			actor := engine.WithAuthority(ctx, engine.Authority{OwnerID: owner, Scopes: []string{"chats:write"}})
			turn, _, err := r.engine.Accept(actor, owner, chat.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "Current date?"})
			if err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(3 * time.Second)
			for time.Now().Before(until) {
				got, readErr := r.chats.GetTurn(ctx, owner, chat.ID, turn.ID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if got.State == "completed" {
					if p.rounds.Load() != 2 || got.OutputMessageID == "" {
						t.Fatal("no tool-backed answer", got)
					}
					return
				}
				if got.State == "failed" || got.State == "unknown_outcome" {
					t.Fatal("mobile tool turn failed", got)
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("no tool-backed mobile answer")

		})
	}
}
