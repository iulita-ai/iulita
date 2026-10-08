package telegram

import (
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
)

func TestMergeMessages(t *testing.T) {
	t.Run("single message short-circuits (identity)", func(t *testing.T) {
		msg := channel.IncomingMessage{
			ChatID: "1", UserID: "7", Locale: "ru", Caps: channel.CapLocations,
			ResolvedUserID: "user-uuid", Text: "hello", MessageID: 42,
			Locations: []channel.LocationAttachment{{Latitude: 1}},
		}
		got := mergeMessages([]channel.IncomingMessage{msg})
		// The single-message path must return the original message unchanged.
		if got.ChatID != "1" || got.UserID != "7" || got.Locale != "ru" ||
			got.Caps != channel.CapLocations || got.ResolvedUserID != "user-uuid" ||
			got.MessageID != 42 || got.Text != "hello" || len(got.Locations) != 1 {
			t.Fatalf("single-message identity broken: %+v", got)
		}
	})

	t.Run("text + pin burst", func(t *testing.T) {
		merged := mergeMessages([]channel.IncomingMessage{
			{ChatID: "1", UserID: "7", Text: "what's the weather here?"},
			{ChatID: "1", UserID: "7", Locations: []channel.LocationAttachment{{Latitude: 52.5, Longitude: 13.4}}},
		})
		if len(merged.Locations) != 1 {
			t.Fatalf("locations lost in merge: %+v", merged)
		}
		if merged.Text != "what's the weather here?" {
			t.Fatalf("text lost: %q", merged.Text)
		}
	})

	t.Run("pin order preserved across burst", func(t *testing.T) {
		merged := mergeMessages([]channel.IncomingMessage{
			{ChatID: "1", Locations: []channel.LocationAttachment{{Latitude: 1}}},
			{ChatID: "1", Locations: []channel.LocationAttachment{{Latitude: 2}}},
			{ChatID: "1", Locations: []channel.LocationAttachment{{Latitude: 3}}},
		})
		if len(merged.Locations) != 3 ||
			merged.Locations[0].Latitude != 1 || merged.Locations[1].Latitude != 2 || merged.Locations[2].Latitude != 3 {
			t.Fatalf("pin order not preserved: %+v", merged.Locations)
		}
	})

	t.Run("venue + photo burst", func(t *testing.T) {
		merged := mergeMessages([]channel.IncomingMessage{
			{ChatID: "1", Locations: []channel.LocationAttachment{{Title: "Cafe"}}},
			{ChatID: "1", Images: []channel.ImageAttachment{{Data: []byte{1}}}},
		})
		if len(merged.Locations) != 1 || merged.Locations[0].Title != "Cafe" {
			t.Fatalf("venue lost: %+v", merged.Locations)
		}
		if len(merged.Images) != 1 {
			t.Fatalf("image lost: %+v", merged.Images)
		}
	})

	t.Run("locale, caps and resolved user propagate from first message", func(t *testing.T) {
		merged := mergeMessages([]channel.IncomingMessage{
			{ChatID: "1", Locale: "ru", Caps: channel.CapButtons | channel.CapLocations, ResolvedUserID: "uuid-1", Text: "a"},
			{ChatID: "1", Locale: "en", Caps: channel.CapHTML, ResolvedUserID: "uuid-2", Text: "b"},
		})
		if merged.Locale != "ru" || merged.Caps != channel.CapButtons|channel.CapLocations || merged.ResolvedUserID != "uuid-1" {
			t.Fatalf("first-wins propagation broken: %+v", merged)
		}
	})

	t.Run("audio appended", func(t *testing.T) {
		merged := mergeMessages([]channel.IncomingMessage{
			{ChatID: "1", Audio: []channel.AudioAttachment{{Format: "ogg"}}},
			{ChatID: "1", Audio: []channel.AudioAttachment{{Format: "mp3"}}},
		})
		if len(merged.Audio) != 2 {
			t.Fatalf("audio lost in merge: %+v", merged.Audio)
		}
	})
}

// TestDebouncer_ImmediateWithZeroWindow mirrors the slack fixture: window 0
// must forward every message (including pins) immediately, unmerged.
func TestDebouncer_ImmediateWithZeroWindow(t *testing.T) {
	var mu sync.Mutex
	var received []channel.IncomingMessage

	d := newDebouncer(0, func(msg channel.IncomingMessage) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	})

	d.add(channel.IncomingMessage{ChatID: "chat1", Text: "hello"})
	d.add(channel.IncomingMessage{ChatID: "chat2", Locations: []channel.LocationAttachment{{Latitude: 52.5, Longitude: 13.4}}})

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(received))
	}
	// The two handler goroutines have no guaranteed completion order — locate
	// the pin message by chat, not by arrival index.
	var pin *channel.IncomingMessage
	for i := range received {
		if received[i].ChatID == "chat2" {
			pin = &received[i]
		}
	}
	if pin == nil || len(pin.Locations) != 1 || pin.Locations[0].Latitude != 52.5 {
		t.Fatalf("pin lost on zero-window path: %+v", received)
	}
}
