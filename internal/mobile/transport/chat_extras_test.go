package transport

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type photoCaptureProvider struct {
	calls  atomic.Int32
	photos atomic.Int32
}

func (p *photoCaptureProvider) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	p.calls.Add(1)
	p.photos.Store(int32(len(r.Images)))
	return llm.Response{Content: "ordinary answer"}, nil
}
func TestChatExtrasPhotosFactsAndSecurity(t *testing.T) {
	h := setupHarness(t)
	h.chats.ConfigureMedia(make([]byte, 32))
	provider := &photoCaptureProvider{}
	// Wait for initialization before replacing the immutable fixture dependencies.
	<-h.server.config.Engine.Ready()
	h.server.config.Engine.Resolve = func(context.Context, string, string) (engine.Profile, error) {
		return engine.Profile{Provider: provider, Revision: 1, Digest: "fixture-model", Images: true}, nil
	}
	h.server.config.Engine.Profiles = func() []llm.AvailableProfile { return []llm.AvailableProfile{{ID: "fixture", Images: true}} }
	token, device := h.login(t, "owner")
	other, _ := h.login(t, "other")
	ctx := tokenContext(token)
	chat := v1.NewChatServiceClient(h.conn)
	extra := v1.NewChatExtrasServiceClient(h.conn)
	c, err := chat.CreateConversation(ctx, &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "photos", Settings: &v1.ConversationSettings{ProfileId: "fixture", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 12)), nil)
	request := &v1.SendPhotosRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "caption", Photos: []*v1.PhotoInput{{Jpeg: buf.Bytes()}, {Jpeg: buf.Bytes()}}}
	turn, err := extra.SendPhotos(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := extra.SendPhotos(ctx, request)
	if err != nil || again.TurnId != turn.TurnId {
		t.Fatal("retry", err)
	}
	changed := proto.Clone(request).(*v1.SendPhotosRequest)
	changed.Text = "different"
	if _, err = extra.SendPhotos(ctx, changed); status.Code(err) != codes.AlreadyExists {
		t.Fatal("payload conflict", err)
	}
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		turn, err = chat.GetTurn(ctx, &v1.GetTurnRequest{Client: clientContext(), ConversationId: c.ConversationId, Lookup: &v1.GetTurnRequest_TurnId{TurnId: turn.TurnId}})
		if err != nil {
			t.Fatal(err)
		}
		if turn.State == v1.TurnState_TURN_STATE_COMPLETED {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if turn.State != v1.TurnState_TURN_STATE_COMPLETED || provider.calls.Load() != 1 || provider.photos.Load() != 2 {
		t.Fatal("provider images or dedup", turn.State, provider.calls.Load(), provider.photos.Load())
	}
	infos, err := extra.ListChatPhotos(ctx, &v1.ListChatPhotosRequest{Client: clientContext(), ConversationId: c.ConversationId, HistoryGeneration: 1, MessageIds: []string{turn.InputMessageId}})
	if err != nil || len(infos.Photos) != 2 {
		t.Fatal(err)
	}
	get := &v1.GetChatPhotoRequest{Client: clientContext(), ConversationId: c.ConversationId, HistoryGeneration: 1, PhotoId: infos.Photos[0].PhotoId}
	if _, err = extra.GetChatPhoto(tokenContext(other), get); status.Code(err) != codes.NotFound {
		t.Fatal("foreign photo", err)
	}
	if _, err = extra.GetChatPhoto(context.Background(), get); status.Code(err) != codes.Unauthenticated {
		t.Fatal("anonymous photo", err)
	}
	fact := &v1.SaveAnswerFactRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, HistoryGeneration: 1, MessageId: turn.OutputMessageId, Text: "edited ordinary fact", ConfirmAccountExport: true}
	a, err := extra.SaveAnswerFact(ctx, fact)
	if err != nil {
		t.Fatal(err)
	}
	b, err := extra.SaveAnswerFact(ctx, fact)
	if err != nil || a.FactId != b.FactId {
		t.Fatal("fact duplicate", err)
	}
	facts, err := h.db.GetRecentFactsByUser(ctx, h.owners["owner"], 100)
	if err != nil || len(facts) != 1 || facts[0].Content != "edited ordinary fact" {
		t.Fatal("fact not visible through account memory", err)
	}
	denied := proto.Clone(fact).(*v1.SaveAnswerFactRequest)
	denied.Operation = operation()
	denied.ConfirmAccountExport = false
	if _, err = extra.SaveAnswerFact(ctx, denied); status.Code(err) != codes.PermissionDenied {
		t.Fatal("unconfirmed export", err)
	}
	if _, err = extra.SaveAnswerFact(tokenContext(other), fact); status.Code(err) != codes.NotFound {
		t.Fatal("foreign fact", err)
	}
	_, err = chat.ClearHistory(ctx, &v1.ClearHistoryRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, ExpectedHistoryGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = extra.GetChatPhoto(ctx, get); status.Code(err) != codes.Aborted {
		t.Fatal("cleared photo", err)
	}
	if _, err = extra.SaveAnswerFact(ctx, fact); err == nil {
		t.Fatal("cleared export replay")
	}
	d, err := h.oauth.Device(ctx, h.owners["owner"], device)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.oauth.RevokeDevice(ctx, h.owners["owner"], device, d.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err = extra.ListChatProfiles(ctx, &v1.ListChatProfilesRequest{Client: clientContext()}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("revoked access admitted", err)
	}
}
