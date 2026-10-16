package devices

import (
	"testing"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPassiveContinuationPreservesAuthorityAndCannotReviveStop(t *testing.T) {
	old := &v1.SessionMirror{SessionId: "same", SessionGeneration: 3, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS}, AllowedPackages: []string{"test.selected"}, ExpiresAt: timestamppb.Now(), ClockId: "old", PolicyRevision: PolicyRevision}
	next := proto.Clone(old).(*v1.SessionMirror)
	next.SessionGeneration++
	next.ClockId = "new"
	next.ExpiresAt = timestamppb.New(time.Now().Add(10 * time.Minute))
	if !passiveContinuation(old, next, "family", "family") {
		t.Fatal("same passive authority cannot rotate")
	}
	cases := map[string]func(*v1.SessionMirror){
		"same generation": func(s *v1.SessionMirror) { s.SessionGeneration = 3 },
		"rollback":        func(s *v1.SessionMirror) { s.SessionGeneration = 2 },
		"other id":        func(s *v1.SessionMirror) { s.SessionId = "other" },
		"extra package":   func(s *v1.SessionMirror) { s.AllowedPackages = append(s.AllowedPackages, "test.other") },
		"other action":    func(s *v1.SessionMirror) { s.AllowedActions = []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_LOCATION} },
		"screen":          func(s *v1.SessionMirror) { s.ScreenContentAllowed = true },
		"image":           func(s *v1.SessionMirror) { s.ScreenshotAllowed = true },
		"digest":          func(s *v1.SessionMirror) { s.ApprovedEnvelopeSha256 = []byte{1} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := proto.Clone(next).(*v1.SessionMirror)
			mutate(bad)
			if passiveContinuation(old, bad, "family", "family") {
				t.Fatal("authority changed during rotation")
			}
		})
	}
	if passiveContinuation(old, next, "first", "second") {
		t.Fatal("different family inherited grant")
	}
	stopped := proto.Clone(old).(*v1.SessionMirror)
	stopped.State = v1.SessionState_SESSION_STATE_REVOKED
	if passiveContinuation(stopped, next, "family", "family") {
		t.Fatal("explicit Stop was revived")
	}
	for _, action := range []v1.ActionKind{v1.ActionKind_ACTION_KIND_UI_CLICK, v1.ActionKind_ACTION_KIND_UI_OBSERVE} {
		ui := proto.Clone(old).(*v1.SessionMirror)
		ui.AllowedActions = []v1.ActionKind{action}
		uiNext := proto.Clone(ui).(*v1.SessionMirror)
		uiNext.SessionGeneration++
		if passiveContinuation(ui, uiNext, "family", "family") {
			t.Fatal("UI authority crossed transport")
		}
	}
}
