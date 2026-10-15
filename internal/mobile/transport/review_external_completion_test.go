package transport

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	mobileexternal "github.com/iulita-ai/iulita/internal/mobile/external"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type exactTemplateSend struct{ instance, chat, text string }
type exactTemplateSender struct {
	mu    sync.Mutex
	sends []exactTemplateSend
}

func (s *exactTemplateSender) SendToInstance(ctx context.Context, instance, chat, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.sends = append(s.sends, exactTemplateSend{instance, chat, text})
	return nil
}
func (s *exactTemplateSender) snapshot() []exactTemplateSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]exactTemplateSend(nil), s.sends...)
}

func canonicalExternalFixture(t *testing.T) (h *harness, sender *exactTemplateSender, bindingID, taskID string) {
	t.Helper()
	adapter := new(mobileexternal.Adapter)
	h = setupHarnessWithOptions(t, devices.Options{Origins: adapter})
	sender = new(exactTemplateSender)
	adapter.Store, adapter.Sender = h.db, sender
	ctx := context.Background()
	instance := &domain.ChannelInstance{ID: "exact-private-instance", Type: domain.ChannelTypeTelegram, Name: "Private fixture", Source: domain.ChannelSourceDashboard, Config: "{}", Enabled: true}
	if err := h.db.CreateChannelInstance(ctx, instance); err != nil {
		t.Fatal(err)
	}
	mapping := &domain.UserChannel{UserID: h.owners["owner"], ChannelInstanceID: instance.ID, ChannelType: instance.Type, ChannelID: "123", ChannelUserID: "123", Enabled: true}
	if err := h.db.BindChannel(ctx, mapping); err != nil {
		t.Fatal(err)
	}
	token, device := h.login(t, "owner")
	conversation, _ := origin(t, h, token, "canonical external fixture")
	binding, err := v1.NewPhoneServiceClient(h.conn).BindExternalOrigin(tokenContext(token), &v1.BindExternalOriginRequest{Client: clientContext(), Operation: operation(), ChannelInstanceId: instance.ID, ChatId: "123", DeviceId: device, ConversationId: conversation, ProfileId: "default"})
	if err != nil {
		t.Fatal(err)
	}
	native := startNative(t, h, token, &v1.SessionMirror{SessionId: uuid.NewString(), SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)), PolicyRevision: devices.PolicyRevision})
	reply, handled, err := h.devices.HandleExternal(ctx, h.server.config.Engine, channel.IncomingMessage{ResolvedUserID: h.owners["owner"], UserID: "123", ChannelInstanceID: instance.ID, ChatID: "123", MessageID: 101, Text: "/phone status"})
	if err != nil || !handled || !strings.Contains(reply, "принят") {
		t.Fatal("canonical external admission", reply, err)
	}
	command := native.command(t)
	native.receipt(t, command, v1.ReceiptState_RECEIPT_STATE_STARTED)
	// Stop automatic polling and publisher ticks. These tests must prove that
	// publication itself observes account changes without another mobile RPC.
	h.server.grpc.Stop()
	h.cancel()
	metadataDB, openErr := sqlitedb.Open(ctx, h.paths[1], false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, clearErr := metadataDB.ExecContext(ctx, `DELETE FROM mobile_external_delivery WHERE task_id=?`, command.TaskId); clearErr != nil {
		t.Fatal(clearErr)
	}
	if closeErr := metadataDB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	sender.mu.Lock()
	sender.sends = nil
	sender.mu.Unlock()
	return h, sender, binding.BindingId, command.TaskId
}

func TestExternalCanonicalAdapterPublishesOnceToExactInstance(t *testing.T) {
	h, sender, _, task := canonicalExternalFixture(t)
	var publishers sync.WaitGroup
	errors := make(chan error, 4)
	for i := 0; i < cap(errors); i++ {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			errors <- h.devices.PublishExternal(context.Background())
		}()
	}
	publishers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	sends := sender.snapshot()
	if len(sends) != 1 || sends[0].instance != "exact-private-instance" || sends[0].chat != "123" || !strings.Contains(sends[0].text, task) || strings.Contains(sends[0].text, "CANARY") {
		t.Fatal("persisted canonical binding did not send fixed metadata exactly once", sends)
	}
}

func TestExternalInvalidOriginsDoNotStarveLaterValidState(t *testing.T) {
	h, sender, binding, task := canonicalExternalFixture(t)
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// One remapped origin can have many historical tasks; this stays within
	// the production limit of sixteen active bindings for the owner.
	invalidBinding := uuid.NewString()
	if _, err = db.ExecContext(ctx, `INSERT INTO mobile_external_bindings SELECT ?,owner_id,device_id,viewer_device_id,channel_type,instance,chat_id,conversation_id,profile_id,authority,?,expires_at,revoked FROM mobile_external_bindings WHERE id=?`, invalidBinding, "old-revision", binding); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		invalidTask, invalidTurn := uuid.NewString(), uuid.NewString()
		if _, err = db.ExecContext(ctx, `INSERT INTO mobile_phone_tasks(id,owner_id,device_id,conversation_id,turn_id,history_generation,action,request_digest,caller_family_id,viewer_device_id,state,session_id,session_generation,created_at,expires_at,consent_revision,destination,approval_authority) SELECT ?,owner_id,device_id,conversation_id,?,history_generation,action,request_digest,caller_family_id,viewer_device_id,'failed',session_id,session_generation,created_at-10000-?,expires_at,consent_revision,destination,approval_authority FROM mobile_phone_tasks WHERE id=?`, invalidTask, invalidTurn, i, task); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO mobile_external_turns VALUES(?,?)`, invalidTurn, invalidBinding); err != nil {
			t.Fatal(err)
		}
	}
	if err = h.devices.PublishExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if sends := sender.snapshot(); len(sends) != 0 {
		t.Fatal("remapped origins received metadata", sends)
	}
	if err = h.devices.PublishExternal(ctx); err != nil {
		t.Fatal(err)
	}
	sends := sender.snapshot()
	if len(sends) != 1 || !strings.Contains(sends[0].text, task) {
		t.Fatal("invalid origins starved the later valid notification", sends)
	}
	if err = h.devices.PublishExternal(ctx); err != nil || len(sender.snapshot()) != 1 {
		t.Fatal("repeated sweep replayed metadata or reactivated an old origin", err)
	}
}

func TestExternalPublicationSynchronizesPasswordAndSecurityEpoch(t *testing.T) {
	for _, password := range []bool{false, true} {
		name := "security_logout"
		if password {
			name = "password_change"
		}
		t.Run(name, func(t *testing.T) {
			h, sender, _, _ := canonicalExternalFixture(t)
			ctx := context.Background()
			owner := h.owners["owner"]
			if password {
				user, lookupErr := h.db.GetUser(ctx, owner)
				if lookupErr != nil {
					t.Fatal(lookupErr)
				}
				user.PasswordHash = "changed-test-only-password-hash"
				if updateErr := h.db.UpdateUser(ctx, user); updateErr != nil {
					t.Fatal(updateErr)
				}
			} else if revokeErr := h.db.RevokeUserSessions(ctx, owner); revokeErr != nil {
				t.Fatal(revokeErr)
			}
			if publishErr := h.devices.PublishExternal(ctx); publishErr != nil {
				t.Fatal(publishErr)
			}
			if sends := sender.snapshot(); len(sends) != 0 {
				t.Fatal("stale account authority published after account change", sends)
			}
		})
	}
}
