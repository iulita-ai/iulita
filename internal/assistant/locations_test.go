package assistant

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/sharelocation"
)

func TestSharedLocationsDirective(t *testing.T) {
	got := sharedLocationsDirective()
	if !strings.Contains(got, "[Location]: lat, lon") || !strings.Contains(got, "share_location") {
		t.Fatalf("directive missing markers: %q", got)
	}
	if !strings.Contains(got, "3 decimals") {
		t.Fatalf("coordinate-privacy guidance missing: %q", got)
	}
	// The header must be truthful on BOTH injection paths (current-message
	// markers and history-only follow-up turns).
	if !strings.Contains(got, "or a recent message in this conversation") {
		t.Fatalf("header not follow-up-accurate: %q", got[:200])
	}
}

func TestDynamicSystemPromptSharedLocationsBlock(t *testing.T) {
	a := newTestAssistant(t, &mockProvider{response: "ok"})
	got := a.dynamicSystemPrompt("my directive", "", "", "profile facts", "2026-10-07", sharedLocationsDirective())
	iDir := strings.Index(got, "## User Directives")
	iLoc := strings.Index(got, "## Shared Locations")
	iProf := strings.Index(got, "## User Profile")
	if iDir < 0 || iLoc < 0 || iProf < 0 {
		t.Fatalf("missing sections in prompt:\n%s", got)
	}
	if iDir >= iLoc || iLoc >= iProf {
		t.Fatalf("Shared Locations block misplaced: directives=%d locations=%d profile=%d", iDir, iLoc, iProf)
	}

	// Without locations the block must be absent.
	got = a.dynamicSystemPrompt("my directive", "", "", "profile facts", "2026-10-07", "")
	if strings.Contains(got, "## Shared Locations") {
		t.Fatalf("Shared Locations block present without locations:\n%s", got)
	}
}

// geoStubSkill is a no-op stand-in named "geolocation" with force triggers,
// so MatchForceTrigger fires without any network access.
type geoStubSkill struct{}

func (geoStubSkill) Name() string        { return "geolocation" }
func (geoStubSkill) Description() string { return "stub geolocation" }
func (geoStubSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (geoStubSkill) Execute(context.Context, json.RawMessage) (string, error) {
	return "stub geo result", nil
}

func TestGeolocationForceToolSuppressedOnPin(t *testing.T) {
	setup := func(t *testing.T) (*Assistant, *[]llm.Request) {
		t.Helper()
		var reqs []llm.Request
		p := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
			reqs = append(reqs, req)
			return llm.Response{Content: "ok"}, nil
		}}
		reg := skill.NewRegistry()
		reg.RegisterWithManifest(geoStubSkill{}, &skill.Manifest{
			Name: "geolocation", ForceTriggers: []string{"where am i"},
		})
		store := newTestStore(t)
		a := New(p, store, reg, "test", "", 200000, zap.NewNop())
		return a, &reqs
	}

	t.Run("pin present: force suppressed", func(t *testing.T) {
		a, reqs := setup(t)
		msg := newTestMsg("chat-pin", "where am i?")
		msg.Locations = []channel.LocationAttachment{{Latitude: 52.5, Longitude: 13.4}}
		if _, err := a.HandleMessage(context.Background(), msg); err != nil {
			// The mock provider returns a plain response; an error here means
			// the forced tool path ran and hit the stub loop.
			t.Fatalf("HandleMessage error: %v", err)
		}
		if len(*reqs) == 0 {
			t.Fatal("no LLM request captured")
		}
		if got := (*reqs)[0].ForceTool; got != "" {
			t.Fatalf("ForceTool = %q, want empty (suppressed on pin turn)", got)
		}
	})

	t.Run("no pin: geolocation forced", func(t *testing.T) {
		a, reqs := setup(t)
		// The mock provider never calls the forced tool, so HandleMessage may
		// return a RequireToolOutcome error — that is not what we assert here.
		if _, err := a.HandleMessage(context.Background(), newTestMsg("chat-plain", "where am i?")); err != nil {
			t.Logf("forced path returned error (expected with mock provider): %v", err)
		}
		if len(*reqs) == 0 {
			t.Fatal("no LLM request captured")
		}
		if got := (*reqs)[0].ForceTool; got != "geolocation" {
			t.Fatalf("ForceTool = %q, want geolocation", got)
		}
	})
}

// assistantFakeSender adapts a channel.LocationSender recording calls for the
// tool-loop test.
type assistantFakeSender struct {
	calls int
}

func (f *assistantFakeSender) CanSendLocations(string) bool { return true }

func (f *assistantFakeSender) SendLocationToChat(_ context.Context, _ string, _ channel.OutboundLocation) (int, error) {
	f.calls++
	return 77, nil
}

func (f *assistantFakeSender) SendVenueToChat(_ context.Context, _ string, _ channel.OutboundVenue) (int, error) {
	f.calls++
	return 78, nil
}

// TestShareLocationInsideToolLoop drives the real share_location skill through
// the assistant tool loop against a fake sender (plan §11 end-to-end slice).
func TestShareLocationInsideToolLoop(t *testing.T) {
	shareSkill := sharelocation.New(zap.NewNop())
	sender := &assistantFakeSender{}
	shareSkill.SetLocationSender(sender)

	calls := 0
	p := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
		calls++
		if calls == 1 {
			if len(req.Tools) == 0 {
				t.Fatal("share_location not offered in tool definitions")
			}
			return llm.Response{ToolCalls: []llm.ToolCall{{
				ID: "call-1", Name: "share_location",
				Input: json.RawMessage(`{"latitude": 52.51627, "longitude": 13.37775, "title": "Brandenburg Gate", "address": "Pariser Platz 1"}`),
			}}}, nil
		}
		return llm.Response{Content: "sent!"}, nil
	}}
	reg := skill.NewRegistry()
	manifest, err := sharelocation.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	reg.RegisterWithManifest(shareSkill, manifest)
	a := New(p, newTestStore(t), reg, "test", "", 200000, zap.NewNop())

	out, err := a.HandleMessage(context.Background(), newTestMsg("chat-share", "send me the Brandenburg Gate on the map"))
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}
	if sender.calls != 1 {
		t.Fatalf("share_location executed %d times, want 1", sender.calls)
	}
	if out == "" {
		t.Fatal("empty final response")
	}
}

func TestRecentSharedLocation(t *testing.T) {
	mk := func(role domain.Role, content string) domain.ChatMessage {
		return domain.ChatMessage{Role: role, Content: content}
	}
	pin := mk(domain.RoleUser, "what's here?\n[Location]: 52.516270, 13.377750")
	old := mk(domain.RoleUser, "[Place]: Old Cafe — Street 1 (52.0, 13.0)")
	filler := mk(domain.RoleUser, "filler")

	tests := []struct {
		name string
		msgs []domain.ChatMessage
		want bool
	}{
		{"empty", nil, false},
		{"recent pin", []domain.ChatMessage{pin}, true},
		{"pin beyond window (oldest first)", []domain.ChatMessage{pin, filler, filler, filler, filler, filler}, false},
		{"assistant marker ignored", []domain.ChatMessage{mk(domain.RoleAssistant, "[Location]: 1, 2")}, false},
		{"old pin within window", []domain.ChatMessage{filler, filler, old}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recentSharedLocation(tt.msgs); got != tt.want {
				t.Fatalf("recentSharedLocation(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestGeolocationForceSuppressedOnFollowUpTurn covers smoke item 4: the pin
// was shared in a PREVIOUS turn; "where am I?" now must not force the
// IP-based geolocation tool.
func TestGeolocationForceSuppressedOnFollowUpTurn(t *testing.T) {
	var reqs []llm.Request
	p := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
		reqs = append(reqs, req)
		return llm.Response{Content: "ok"}, nil
	}}
	reg := skill.NewRegistry()
	reg.RegisterWithManifest(geoStubSkill{}, &skill.Manifest{
		Name: "geolocation", ForceTriggers: []string{"where am i"},
	})
	a := New(p, newTestStore(t), reg, "test", "", 200000, zap.NewNop())

	// Turn 1: shared pin (simulating the channel-injected marker line).
	pinMsg := newTestMsg("chat-followup", "[Location]: 52.516270, 13.377750")
	pinMsg.Locations = []channel.LocationAttachment{{Latitude: 52.51627, Longitude: 13.37775}}
	if _, err := a.HandleMessage(context.Background(), pinMsg); err != nil {
		t.Fatalf("pin turn error: %v", err)
	}

	// Turn 2: follow-up question, no Locations on the message itself.
	reqs = nil
	if _, err := a.HandleMessage(context.Background(), newTestMsg("chat-followup", "where am i?")); err != nil {
		t.Logf("follow-up returned error (acceptable with mock provider): %v", err)
	}
	if len(reqs) == 0 {
		t.Fatal("no LLM request captured on follow-up")
	}
	if got := reqs[0].ForceTool; got != "" {
		t.Fatalf("ForceTool = %q on follow-up turn, want empty (pin in history)", got)
	}
}

// TestScrubPreview enforces the §12/§13 rule at the preview sink: geocode
// results and OSM links embed full-precision coordinates that must never
// reach INFO+ logs.
func TestScrubPreview(t *testing.T) {
	in := "Brandenburg Gate, Pariser Platz 1 — 52.516270, 13.377750 (data © OpenStreetMap contributors)\nhttps://www.openstreetmap.org/?mlat=-33.865143&mlon=-70.995777"
	out := scrubPreview(in)
	if regexp.MustCompile(`-?\d{1,3}\.\d{4,}`).MatchString(out) {
		t.Fatalf("coordinate leaked through scrub: %s", out)
	}
	if !strings.Contains(out, "Brandenburg Gate") || !strings.Contains(out, "[coords]") {
		t.Fatalf("scrub mangled non-coordinate content: %s", out)
	}

	// Single 4+-decimal numbers from UNRELATED skills (exchange rates, math)
	// must survive — only coordinate pairs and OSM links are redacted.
	forex := "1 EUR = 1.08532 USD"
	if got := scrubPreview(forex); got != forex {
		t.Fatalf("lone decimal over-scrubbed: %q", got)
	}

	// Weather-failure strings embed the forecast URL with latitude=/longitude=
	// query params — those must be redacted too.
	weatherErr := `Unable to fetch weather for "52.516270, 13.377750": HTTP request failed: Get "https://api.open-meteo.com/v1/forecast?latitude=52.5163&longitude=13.3778&daily=..."`
	out = scrubPreview(weatherErr)
	if regexp.MustCompile(`-?\d{1,3}\.\d{4,}`).MatchString(out) {
		t.Fatalf("weather-URL coordinate leaked through scrub: %s", out)
	}
}

// TestGeolocationIPTriggerNotSuppressedAfterPin covers the trigger-scoped
// suppression: an explicit IP question keeps the forced geolocation call even
// while a shared pin sits in recent history (the pin is irrelevant to an IP
// question).
func TestGeolocationIPTriggerNotSuppressedAfterPin(t *testing.T) {
	var reqs []llm.Request
	p := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
		reqs = append(reqs, req)
		return llm.Response{Content: "ok"}, nil
	}}
	reg := skill.NewRegistry()
	// Trigger order mirrors the real SKILL.md: bare "geolocation" FIRST, then
	// IP phrases — the two-pass matcher must still report an IP-scoped trigger.
	reg.RegisterWithManifest(geoStubSkill{}, &skill.Manifest{
		Name:          "geolocation",
		ForceTriggers: []string{"geolocation", "where am i", "my ip", "ip geolocation"},
	})
	a := New(p, newTestStore(t), reg, "test", "", 200000, zap.NewNop())

	// Turn 1: shared pin (marker line persisted into history).
	pinMsg := newTestMsg("chat-ip", "[Location]: 52.516270, 13.377750")
	pinMsg.Locations = []channel.LocationAttachment{{Latitude: 52.51627, Longitude: 13.37775}}
	if _, err := a.HandleMessage(context.Background(), pinMsg); err != nil {
		t.Fatalf("pin turn error: %v", err)
	}

	// Turn 1b: mixed phrase — "ip geolocation" is IP-scoped and must win over
	// the bare "geolocation" trigger (two-pass matching), keeping the force.
	reqs = nil
	if _, err := a.HandleMessage(context.Background(), newTestMsg("chat-ip", "what's my ip geolocation?")); err != nil {
		t.Logf("mixed-phrase turn returned error (acceptable with mock provider): %v", err)
	}
	if len(reqs) == 0 {
		t.Fatal("no LLM request captured on mixed-phrase turn")
	}
	if got := reqs[0].ForceTool; got != "geolocation" {
		t.Fatalf("ForceTool = %q on mixed ip-geolocation turn, want geolocation", got)
	}

	// Turn 2: explicit IP question — the "my ip" trigger must stay forced.
	reqs = nil
	if _, err := a.HandleMessage(context.Background(), newTestMsg("chat-ip", "what's my ip address?")); err != nil {
		t.Logf("ip turn returned error (acceptable with mock provider): %v", err)
	}
	if len(reqs) == 0 {
		t.Fatal("no LLM request captured")
	}
	if got := reqs[0].ForceTool; got != "geolocation" {
		t.Fatalf("ForceTool = %q, want geolocation (IP trigger must not be suppressed)", got)
	}
}

// TestSharedLocationsDirectiveOnFollowUpTurn pins the §12 guidance carrier:
// the directive must ALSO be injected when the pin sits in recent history —
// the "yes, remember it" follow-up turn is exactly when the coordinate-privacy
// rules are needed.
func TestSharedLocationsDirectiveOnFollowUpTurn(t *testing.T) {
	var prompts []string
	p := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
		prompts = append(prompts, req.SystemPrompt)
		return llm.Response{Content: "ok"}, nil
	}}
	reg := skill.NewRegistry()
	a := New(p, newTestStore(t), reg, "test", "", 200000, zap.NewNop())

	// Turn 1: shared pin.
	pinMsg := newTestMsg("chat-dir", "[Location]: 52.516270, 13.377750")
	pinMsg.Locations = []channel.LocationAttachment{{Latitude: 52.51627, Longitude: 13.37775}}
	if _, err := a.HandleMessage(context.Background(), pinMsg); err != nil {
		t.Fatalf("pin turn error: %v", err)
	}

	// Turn 2: plain follow-up (no Locations) — directive must still be present
	// with the coordinate-privacy guidance.
	prompts = nil
	if _, err := a.HandleMessage(context.Background(), newTestMsg("chat-dir", "yes, remember this place")); err != nil {
		t.Fatalf("follow-up error: %v", err)
	}
	if len(prompts) == 0 {
		t.Fatal("no LLM request captured")
	}
	if !strings.Contains(prompts[0], "## Shared Locations") {
		t.Fatal("Shared Locations directive missing on follow-up turn")
	}
	if !strings.Contains(prompts[0], "3 decimals") {
		t.Fatal("coordinate-privacy rounding guidance missing on follow-up turn")
	}
}
