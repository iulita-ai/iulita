package sharelocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/iulita-ai/iulita/internal/channel"
	telegramch "github.com/iulita-ai/iulita/internal/channel/telegram"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/skill"
)

// fakeSender is a scriptable channel.LocationSender.
type fakeSender struct {
	canSend    bool
	pinErr     error
	venueErr   error
	gotPin     *channel.OutboundLocation
	gotVenue   *channel.OutboundVenue
	pinCalls   int
	venueCalls int
}

func (f *fakeSender) CanSendLocations(string) bool { return f.canSend }

func (f *fakeSender) SendLocationToChat(_ context.Context, _ string, loc channel.OutboundLocation) (int, error) {
	f.pinCalls++
	f.gotPin = &loc
	if f.pinErr != nil {
		return 0, f.pinErr
	}
	return 11, nil
}

func (f *fakeSender) SendVenueToChat(_ context.Context, _ string, v channel.OutboundVenue) (int, error) {
	f.venueCalls++
	f.gotVenue = &v
	if f.venueErr != nil {
		return 0, f.venueErr
	}
	return 12, nil
}

// busCapture captures published events.
type busCapture struct {
	events []eventbus.Event
}

func newBusCapture(t *testing.T) (*eventbus.Bus, *busCapture) {
	t.Helper()
	bus := eventbus.New(zap.NewNop())
	captured := &busCapture{}
	bus.Subscribe(eventbus.LocationSent, func(_ context.Context, evt eventbus.Event) error {
		captured.events = append(captured.events, evt)
		return nil
	})
	return bus, captured
}

func execute(t *testing.T, s *Skill, chatID, args string) string {
	t.Helper()
	ctx := context.Background()
	if chatID != "" {
		ctx = skill.WithChatID(ctx, chatID)
	}
	out, err := s.Execute(ctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return out
}

func TestMetadata(t *testing.T) {
	s := New(zap.NewNop())
	if s.Name() != "share_location" {
		t.Fatalf("name = %q", s.Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(s.InputSchema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatal("schema top-level type must be object (DeepSeek compatibility)")
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["latitude"]; !ok {
		t.Fatal("schema missing latitude")
	}
	if _, ok := props["longitude"]; !ok {
		t.Fatal("schema missing longitude")
	}
}

// TestSchemaHasNoChatIDParam guards D12: no cross-chat addressing parameter.
func TestSchemaHasNoChatIDParam(t *testing.T) {
	s := New(zap.NewNop())
	schema := string(s.InputSchema())
	for _, forbidden := range []string{"chat_id", "channel", "target", "chat"} {
		if strings.Contains(schema, fmt.Sprintf("%q", forbidden)) {
			t.Fatalf("schema must not contain addressing key %q: %s", forbidden, schema)
		}
	}
}

func TestExecute_PinAndVenue(t *testing.T) {
	s := New(zap.NewNop())
	f := &fakeSender{canSend: true}
	s.SetLocationSender(f)
	bus, captured := newBusCapture(t)
	s.SetBus(bus)

	out := execute(t, s, "100", `{"latitude": 52.51627, "longitude": 13.37775}`)
	if !strings.Contains(out, "sent above") || f.pinCalls != 1 || f.venueCalls != 0 {
		t.Fatalf("pin send broken: out=%q pin=%d venue=%d", out, f.pinCalls, f.venueCalls)
	}
	if len(captured.events) != 1 || captured.events[0].Payload.(eventbus.LocationSentPayload) != (eventbus.LocationSentPayload{Kind: "pin", Outcome: "sent"}) {
		t.Fatalf("pin event wrong: %+v", captured.events)
	}

	out = execute(t, s, "100", `{"latitude": 52.51627, "longitude": 13.37775, "title": "Brandenburg Gate", "address": "Pariser Platz 1"}`)
	if !strings.Contains(out, "venue card sent above") || !strings.Contains(out, "Brandenburg Gate") || f.venueCalls != 1 {
		t.Fatalf("venue send broken: %q", out)
	}
	if got := captured.events[len(captured.events)-1].Payload.(eventbus.LocationSentPayload); got.Kind != "venue" || got.Outcome != "sent" {
		t.Fatalf("venue event wrong: %+v", got)
	}
}

func TestExecute_VenueDowngrade(t *testing.T) {
	s := New(zap.NewNop())
	f := &fakeSender{canSend: true}
	s.SetLocationSender(f)
	execute(t, s, "100", `{"latitude": 1, "longitude": 2, "title": "Only Title"}`)
	execute(t, s, "100", `{"latitude": 1, "longitude": 2, "address": "Only Address"}`)
	if f.pinCalls != 2 || f.venueCalls != 0 {
		t.Fatalf("venue downgrade to pin broken: pin=%d venue=%d", f.pinCalls, f.venueCalls)
	}
}

func TestExecute_InvalidCoordinates(t *testing.T) {
	s := New(zap.NewNop())
	f := &fakeSender{canSend: true}
	s.SetLocationSender(f)
	for _, args := range []string{
		`{"latitude": 91, "longitude": 2}`,
		`{"latitude": -90.5, "longitude": 2}`,
		`{"latitude": 1, "longitude": 181}`,
		`{"latitude": 0, "longitude": 0}`,
		// Missing or null coordinate must NOT silently become 0 (off-Greenwich pin).
		`{"latitude": 48.8584}`,
		`{"longitude": 2.2945}`,
		`{"latitude": 48.8584, "longitude": null}`,
	} {
		// NaN/Inf cannot appear in valid JSON; range, null-island and omission cases can.
		out := execute(t, s, "100", args)
		if !strings.Contains(out, "Invalid coordinates") {
			t.Fatalf("args %s: expected invalid-coordinates refusal, got %q", args, out)
		}
	}
	if f.pinCalls+f.venueCalls != 0 {
		t.Fatal("sender must not be called on invalid coordinates")
	}
}

func TestExecute_AccuracyClamp(t *testing.T) {
	s := New(zap.NewNop())
	f := &fakeSender{canSend: true}
	s.SetLocationSender(f)
	execute(t, s, "100", `{"latitude": 1, "longitude": 2, "accuracy": 5000}`)
	if f.gotPin.Accuracy != 1500 {
		t.Fatalf("accuracy not clamped: %v", f.gotPin.Accuracy)
	}
}

func TestExecute_Fallback(t *testing.T) {
	link := "https://www.openstreetmap.org/?mlat=52.516270&mlon=13.377750"

	s := New(zap.NewNop())
	s.SetLocationSender(&fakeSender{canSend: false})
	if out := execute(t, s, "100", `{"latitude": 52.51627, "longitude": 13.37775}`); !strings.Contains(out, link) {
		t.Fatalf("unsupported-chat fallback missing link: %q", out)
	}

	s2 := New(zap.NewNop()) // nil sender
	if out := execute(t, s2, "100", `{"latitude": 52.51627, "longitude": 13.37775}`); !strings.Contains(out, link) {
		t.Fatalf("nil-sender fallback missing link: %q", out)
	}

	s3 := New(zap.NewNop())
	s3.SetLocationSender(&fakeSender{canSend: true})
	if out := execute(t, s3, "", `{"latitude": 52.51627, "longitude": 13.37775}`); !strings.Contains(out, link) {
		t.Fatalf("empty-chat fallback missing link: %q", out)
	}

	s4 := New(zap.NewNop())
	s4.SetLocationSender(&fakeSender{canSend: true, pinErr: errors.New("telegram: 403 bot can't initiate conversation")})
	if out := execute(t, s4, "100", `{"latitude": 52.51627, "longitude": 13.37775}`); !strings.Contains(out, link) {
		t.Fatalf("send-error fallback missing link: %q", out)
	}
}

func TestExecute_Capped(t *testing.T) {
	s := New(zap.NewNop())
	s.SetLocationSender(&fakeSender{canSend: true, pinErr: telegramch.ErrLocationCapped})
	out := execute(t, s, "100", `{"latitude": 52.51627, "longitude": 13.37775}`)
	if !strings.Contains(out, "limit reached") || !strings.Contains(out, "openstreetmap.org") {
		t.Fatalf("capped refusal broken: %q", out)
	}
}

func TestSecretScan(t *testing.T) {
	s := New(zap.NewNop())
	f := &fakeSender{canSend: true}
	s.SetLocationSender(f)
	out := execute(t, s, "100", `{"latitude": 1, "longitude": 2, "title": "leak", "address": "api_key=abcdefgh12345678"}`)
	if !strings.Contains(out, "secret") || f.venueCalls != 0 {
		t.Fatalf("secret scan did not refuse: %q venueCalls=%d", out, f.venueCalls)
	}
}

func TestEventPayloadCoordinateFree(t *testing.T) {
	s := New(zap.NewNop())
	f := &fakeSender{canSend: false} // fallback outcome
	s.SetLocationSender(f)
	bus, captured := newBusCapture(t)
	s.SetBus(bus)
	execute(t, s, "100", `{"latitude": 52.51627, "longitude": 13.37775}`)
	if len(captured.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(captured.events))
	}
	re := regexp.MustCompile(`-?\d{1,3}\.\d{4,}`)
	if re.MatchString(fmt.Sprint(captured.events[0].Payload)) {
		t.Fatalf("coordinate-shaped value leaked into payload: %+v", captured.events[0].Payload)
	}
}

func TestMapLinkShort(t *testing.T) {
	link := mapLink(52.51627, 13.37775)
	if len(link) > 70 {
		t.Fatalf("map link too long for console wrap: %q (%d)", link, len(link))
	}
	if !strings.HasPrefix(link, "https://www.openstreetmap.org/?mlat=") {
		t.Fatalf("unexpected link shape: %q", link)
	}
}

// TestLogHygieneNoCoordinates asserts no coordinate-shaped value is logged at
// INFO+ across share_location outcomes (plan §13 log rules).
func TestLogHygieneNoCoordinates(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	s := New(zap.New(core))
	s.SetLocationSender(&fakeSender{
		canSend:  true,
		pinErr:   telegramch.ErrLocationCapped,
		venueErr: errors.New("boom"),
	})

	// Capped path (Warn) and fallback path (unsupported chat via empty chatID).
	execute(t, s, "100", `{"latitude": 52.51627, "longitude": 13.37775}`)
	execute(t, s, "", `{"latitude": -33.865143, "longitude": -70.995777}`)

	// Generic send-failure path: Error level with chat_id and kind correlation
	// fields, still no coordinates (plan §13).
	execute(t, s, "100", `{"latitude": 1.5, "longitude": 2.5, "title": "T", "address": "A"}`)

	// Secret-refusal path: Warn with chat_id + kind (plan §13 log table).
	execute(t, s, "100", `{"latitude": 1.5, "longitude": 2.5, "title": "k", "address": "api_key=abcdefgh12345678"}`)

	re := regexp.MustCompile(`-?\d{1,3}\.\d{4,}`)
	for _, entry := range logs.All() {
		if re.MatchString(entry.Message) {
			t.Fatalf("coordinate-shaped log message: %q", entry.Message)
		}
		for _, f := range entry.Context {
			if re.MatchString(fmt.Sprint(f.Interface)) || re.MatchString(f.String) {
				t.Fatalf("coordinate-shaped log field %s: %v", f.Key, f.Interface)
			}
		}
	}

	// Secret-scan refusal Warn must carry chat_id/kind correlation fields too.
	secretEntry := logs.FilterMessage("share_location refused: suspected secret in venue text")
	if secretEntry.Len() != 1 {
		t.Fatalf("expected one secret-refusal Warn log, got %d", secretEntry.Len())
	}
	secretFields := secretEntry.All()[0].ContextMap()
	if secretFields["chat_id"] != "100" || secretFields["kind"] == nil {
		t.Fatalf("secret-refusal log missing correlation fields: %+v", secretFields)
	}

	errEntry := logs.FilterMessage("native location send failed, falling back to map link")
	if errEntry.Len() != 1 {
		t.Fatalf("expected exactly one send-failure Error log, got %d", errEntry.Len())
	}
	fields := errEntry.All()[0].ContextMap()
	if fields["chat_id"] != "100" || fields["kind"] != "venue" {
		t.Fatalf("send-failure log missing correlation fields: %+v", fields)
	}
}
