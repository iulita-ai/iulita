package ephemeral

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTelemetryHistoryDoesNotExtendTaskOrModelTTL(t *testing.T) {
	history, err := NewTelemetry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	history.now = func() time.Time { return now }
	binding := Binding{Owner: uuid.NewString(), Device: uuid.NewString(), Task: uuid.NewString(), Destination: "mobile.telemetry:" + uuid.NewString(), ConsentRevision: "mobile-telemetry-v1", Category: "notifications"}
	if err = history.Put("history", binding, []byte("PRIVATE_HISTORY_CANARY"), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	normal, _ := New()
	normal.now = history.now
	if err = normal.Put("history", binding, []byte("PRIVATE_HISTORY_CANARY"), now.Add(24*time.Hour)); err == nil {
		t.Fatal("normal task TTL was extended")
	}
	model := binding
	model.Category = "derived"
	if err = history.Put("model", model, []byte("PRIVATE_DERIVED_CANARY"), now.Add(time.Hour)); err == nil {
		t.Fatal("model output got history TTL")
	}
	now = now.Add(24 * time.Hour)
	history.Sweep()
	if _, _, err = history.Get("history", binding); err == nil {
		t.Fatal("expired history survived")
	}
}
