package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

type capturedBusinessAPI struct {
	channel *Channel
	before  func()
}

func (a *capturedBusinessAPI) GetConnection(ctx context.Context, key business.Key) (business.RemoteConnection, error) {
	return a.channel.BusinessConnection(ctx, key)
}
func (a *capturedBusinessAPI) Execute(ctx context.Context, request business.Request) (business.Receipt, error) {
	// Model Manager.Execute's pointer capture: removing/replacing the running
	// entry after this point does not destroy this still-reachable old transport.
	captured := a.channel
	if a.before != nil {
		a.before()
	}
	return captured.ExecuteBusiness(ctx, request)
}

type nativeRuntimeRepository struct {
	*businessTransportMemory
	afterGet func(context.Context)
}

func (r *nativeRuntimeRepository) Get(ctx context.Context, key business.Key) (*business.State, error) {
	state, err := r.businessTransportMemory.Get(ctx, key)
	if r.afterGet != nil {
		r.afterGet(ctx)
	}
	return state, err
}

func TestBusinessNativeAdmissionRequiresCurrentRuntimeForTextAndMultipart(t *testing.T) {
	for _, method := range []business.Method{business.SendText, business.SetProfilePhoto} {
		for _, cut := range []string{"running", "captured-stop", "captured-replacement", "wire-stop", "missing-guard", "after-admission-stop"} {
			t.Run(string(method)+"/"+cut, func(t *testing.T) {
				ctx := context.Background() // Independent dashboard lifetime remains live.
				nativeCalls := 0
				runtime := ""
				c := businessTestChannel(t, func(request *http.Request) (*http.Response, error) {
					if strings.HasSuffix(request.URL.Path, "getBusinessConnection") {
						return businessHTTPResponse(200, `{"ok":true,"result":{"id":"conn","user":{"id":101},"user_chat_id":101,"is_enabled":true,"rights":{"can_reply":true,"can_edit_profile_photo":true}}}`), nil
					}
					nativeCalls++
					if cut == "after-admission-stop" {
						runtime = ""
					}
					if method == business.SetProfilePhoto {
						if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data;") {
							t.Fatal("positive fixture did not traverse multipart wire")
						}
						return businessHTTPResponse(200, `{"ok":true,"result":true}`), nil
					}
					return businessHTTPResponse(200, `{"ok":true,"result":{"message_id":500,"business_connection_id":"conn","chat":{"id":303}}}`), nil
				})
				repo := &nativeRuntimeRepository{businessTransportMemory: &businessTransportMemory{states: map[business.Key]business.State{}}}
				crypto, err := config.NewEncryptor(make([]byte, 32))
				if err != nil {
					t.Fatal(err)
				}
				vault, err := business.NewMediaVault(t.TempDir(), crypto)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if closeErr := vault.Close(); closeErr != nil {
						t.Error(closeErr)
					}
				})
				api := &capturedBusinessAPI{channel: c}
				service := business.New(repo, api, nil, business.Options{LiveWrites: true, RuntimeGeneration: "runtime", RestoreGeneration: "restore", Media: vault})
				c.SetBusinessService(service)
				scope := business.Scope{Key: business.Key{InstanceID: c.instanceID, BotID: c.botID, ConnectionID: "conn"}, OwnerID: "owner"}
				remote := business.RemoteConnection{Key: scope.Key, OwnerTelegramID: 101, OwnerChatID: 101, Enabled: true, Rights: business.Rights{CanReply: true, CanEditProfilePhoto: true}}
				if handleErr := service.Handle(ctx, business.Event{Key: scope.Key, OwnerID: scope.OwnerID, Generation: c.businessDeliveryGeneration(), UpdateID: 1, Kind: "connection", Connection: &remote}); handleErr != nil {
					t.Fatal(handleErr)
				}
				state, err := repo.Get(ctx, scope.Key)
				if err != nil {
					t.Fatal(err)
				}
				policy := business.PeerPolicy{Enabled: true, Consent: true, Mode: business.ModeConfirm, AllowedMethods: []business.Method{method}}
				if policyErr := service.Configure(ctx, business.ConfigureInput{Scope: scope, ExpectedRevision: state.Connection.Epoch, Policy: policy}); policyErr != nil {
					t.Fatal(policyErr)
				}
				params := business.Params{"text": json.RawMessage(`"Scoped approved reply"`)}
				if method == business.SendText {
					scope.PeerID = 303
					if policyErr := service.Configure(ctx, business.ConfigureInput{Scope: scope, Policy: policy}); policyErr != nil {
						t.Fatal(policyErr)
					}
					if handleErr := service.Handle(ctx, business.Event{Key: scope.Key, OwnerID: scope.OwnerID, PeerID: scope.PeerID, Generation: c.businessDeliveryGeneration(), UpdateID: 2, Kind: "message", Message: &business.Message{ID: 10, SenderID: 303, Date: time.Now().UTC(), Revision: 2, Text: "Current peer request"}}); handleErr != nil {
						t.Fatal(handleErr)
					}
				} else {
					if updateErr := repo.Update(ctx, scope.Key, func(st *business.State) error { st.Connection.OwnerSecurityEpoch = 1; return nil }); updateErr != nil {
						t.Fatal(updateErr)
					}
					state, err = repo.Get(ctx, scope.Key)
					if err != nil {
						t.Fatal(err)
					}
					var data bytes.Buffer
					if encodeErr := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); encodeErr != nil {
						t.Fatal(encodeErr)
					}
					ref, putErr := vault.Put(ctx, scope, "fixture.png", "image/png", data.Bytes(), business.MediaAuthorityFor(state.Connection, nil))
					if putErr != nil {
						t.Fatal(putErr)
					}
					photo, marshalErr := json.Marshal(map[string]string{"type": "static", "photo": ref})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					params = business.Params{"photo": photo}
				}
				action, err := service.Prepare(ctx, business.PrepareInput{Scope: scope, Method: method, Params: params})
				if err != nil {
					t.Fatal(err)
				}
				if _, approvalErr := service.Approve(ctx, business.ApprovalInput{Scope: scope, ActionID: action.ID, Digest: action.Digest, Step: 1}); approvalErr != nil {
					t.Fatal(approvalErr)
				}
				generation := c.businessDeliveryGeneration()
				runtime = generation
				checks := 0
				wireAuthorityStops := 0
				repo.afterGet = func(readCtx context.Context) {
					if cut == "wire-stop" && readCtx.Value(businessDispatchGuardKey{}) != nil && (method == business.SendText || checks == 3) {
						runtime = ""
						wireAuthorityStops++
					}
				}
				c.SetBusinessRuntimeGuard(func(observed business.Scope) string {
					if observed != scope {
						t.Fatal("runtime guard lost exact scope")
					}
					checks++
					return runtime
				})
				if cut == "missing-guard" {
					c.SetBusinessRuntimeGuard(nil)
				}
				api.before = func() {
					switch cut {
					case "captured-stop":
						runtime = ""
					case "captured-replacement":
						runtime = "replacement-generation"
					}
				}
				result, executeErr := service.Execute(ctx, scope, action.ID)
				if cut == "running" || cut == "after-admission-stop" {
					if executeErr != nil || result == nil || result.State != "sent" || nativeCalls != 1 || checks < 2 {
						t.Fatalf("positive wire fixture failed: action=%v err=%v calls=%d checks=%d", result, executeErr, nativeCalls, checks)
					}
				} else {
					var rejection *business.APIError
					if !errors.As(executeErr, &rejection) || !rejection.Rejected || nativeCalls != 0 || result == nil || result.State != "failed" || ctx.Err() != nil {
						t.Fatalf("captured stale transport admitted: action=%v err=%v calls=%d", result, executeErr, nativeCalls)
					}
					if cut == "wire-stop" && (checks != 4 || wireAuthorityStops != 1) {
						t.Fatalf("actual final wire guard not reached: %d", checks)
					}
				}
				expectedCalls := nativeCalls
				if _, repeatErr := service.Execute(ctx, scope, action.ID); repeatErr == nil || nativeCalls != expectedCalls {
					t.Fatal("terminal action automatically replayed")
				}
			})
		}
	}
}
