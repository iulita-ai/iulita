package telegrambusiness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestFinancialIntentNeedsExplicitCapsTwoStepsAndReservesUnknownSpend(t *testing.T) {
	f := setup(t, true)
	scope := Scope{Key: f.scope.Key, OwnerID: "alice"}
	f.api.remote.Rights.CanTransferStars = true
	if checkErr := f.service.Handle(context.Background(), Event{Key: scope.Key, OwnerID: "alice", Generation: "generation", UpdateID: 3, Kind: "connection", Connection: &f.api.remote}); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := f.service.Configure(context.Background(), ConfigureInput{Scope: scope, ExpectedRevision: currentEpoch(t, f), Policy: PeerPolicy{Enabled: true, Consent: true, Mode: ModeConfirm, AllowedMethods: []Method{TransferStars}}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	in := PrepareInput{Scope: scope, Method: TransferStars, Params: Params{"star_count": json.RawMessage(`10`)}}
	if _, e := f.service.Prepare(context.Background(), in); !errors.Is(e, ErrUnavailable) {
		t.Fatal("financial caps default was permissive", e)
	}
	f.service.options.FinancialLimits = FinancialLimits{DailyOperations: 1, DailyStars: 100}
	a, e := f.service.Prepare(context.Background(), in)
	if e != nil || a.RequiredSteps != 2 {
		t.Fatal(a, e)
	}
	if _, e = f.service.Approve(context.Background(), ApprovalInput{Scope: scope, ActionID: a.ID, Digest: a.Digest, Step: 1}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Execute(context.Background(), scope, a.ID); e == nil {
		t.Fatal("one-step financial action executed")
	}
	if f.api.calls.Load() != 0 {
		t.Fatal("financial call before second confirmation")
	}
	if _, e = f.service.Approve(context.Background(), ApprovalInput{Scope: scope, ActionID: a.ID, Digest: a.Digest, Step: 2}); e != nil {
		t.Fatal(e)
	}
	f.api.execute = func(_ context.Context, r Request) (Receipt, error) {
		payload, payloadErr := r.Payload()
		if payloadErr != nil || r.MethodName() != "transferBusinessAccountStars" || payload["business_connection_id"] != "conn" || payload["chat_id"] != nil {
			t.Error("wrong Stars destination/authority", payloadErr)
		}
		return Receipt{}, errors.New("unknown result")
	}
	if _, e = f.service.Execute(context.Background(), scope, a.ID); !errors.Is(e, ErrUnknownOutcome) {
		t.Fatal(e)
	}
	if _, e = f.service.Prepare(context.Background(), in); !errors.Is(e, ErrQuota) {
		t.Fatal("uncertain financial spend was released", e)
	}
	if f.api.calls.Load() != 1 {
		t.Fatal("financial operation repeated")
	}
	// Erasing private intent parameters and explicitly resetting consent must
	// not refund a same-day transfer whose outcome remains unknown.
	f.service.options.FinancialLimits = FinancialLimits{DailyOperations: 10, DailyStars: 15}
	if e = f.service.Purge(context.Background(), scope); e != nil {
		t.Fatal(e)
	}
	if e = f.service.Rebind(context.Background(), "alice", scope.Key, currentEpoch(t, f)); e != nil {
		t.Fatal(e)
	}
	if e = f.service.Configure(context.Background(), ConfigureInput{Scope: scope, ExpectedRevision: currentEpoch(t, f), Policy: PeerPolicy{Enabled: true, Consent: true, Mode: ModeConfirm, AllowedMethods: []Method{TransferStars}}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Prepare(context.Background(), in); !errors.Is(e, ErrQuota) {
		t.Fatal("purge/reset refunded unknown Stars reservation", e)
	}
}

func TestDraftOnlyModeCannotPrepareOrSendExternalReply(t *testing.T) {
	f := setup(t, true)
	if checkErr := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error { st.Connection.Policy.Mode = ModeDraft; return nil }); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, e := f.service.Prepare(context.Background(), PrepareInput{Scope: f.scope, Method: SendText, Params: Params{"text": json.RawMessage(`"not approved by draft-only mode"`)}}); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if f.api.calls.Load() != 0 {
		t.Fatal("draft-only external action")
	}
}

func TestMediaReferenceMustHaveSourceInThisExactPeer(t *testing.T) {
	f := setup(t, false)
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	peer := st.Peers[99]
	policy := peer.Policy
	policy.AllowedMethods = append(policy.AllowedMethods, SendPhoto)
	if checkErr := f.service.Configure(context.Background(), ConfigureInput{Scope: f.scope, ExpectedRevision: peer.Revision, Policy: policy}); checkErr != nil {
		t.Fatal(checkErr)
	}
	in := PrepareInput{Scope: f.scope, Method: SendPhoto, Params: Params{"photo": json.RawMessage(`"other_peer_file"`)}}
	if _, e := f.service.Prepare(context.Background(), in); !errors.Is(e, ErrDenied) {
		t.Fatal("cross-peer file reference admitted", e)
	}
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 4, Kind: "message", Message: &Message{ID: 11, SenderID: 99, Date: f.now, Revision: 4, Text: "received photo", Media: []Media{{FileID: "same_peer_file", Kind: "photo", Bytes: 128}}}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	in.Params["photo"] = json.RawMessage(`"same_peer_file"`)
	if _, e := f.service.Prepare(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	if f.api.calls.Load() != 0 {
		t.Fatal("preparing media performed API write")
	}
}

func TestRepostCannotUseAnotherOwnersConnectedAccount(t *testing.T) {
	f := setup(t, false)
	f.api.remote.Rights.CanManageStories = true
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", Generation: "generation", UpdateID: 3, Kind: "connection", Connection: &f.api.remote}); checkErr != nil {
		t.Fatal(checkErr)
	}
	scope := Scope{Key: f.scope.Key, OwnerID: "alice"}
	if checkErr := f.service.Configure(context.Background(), ConfigureInput{Scope: scope, ExpectedRevision: currentEpoch(t, f), Policy: PeerPolicy{Enabled: true, Consent: true, Mode: ModeConfirm, AllowedMethods: []Method{RepostStory}}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	other := Key{InstanceID: "test", BotID: 7, ConnectionID: "other"}
	if checkErr := f.repo.Update(context.Background(), other, func(st *State) error { st.Connection.OwnerID = "bob"; st.Connection.OwnerTelegramID = 100; return nil }); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, e := f.service.Prepare(context.Background(), PrepareInput{Scope: scope, Method: RepostStory, Params: Params{"from_chat_id": json.RawMessage(`100`), "from_story_id": json.RawMessage(`5`), "active_period": json.RawMessage(`86400`)}}); !errors.Is(e, ErrDenied) {
		t.Fatal("other owner's source story admitted", e)
	}
}

func TestZeroStarsBudgetDisablesEvenFreeGiftOperations(t *testing.T) {
	for _, method := range []Method{ConvertGift, UpgradeGift} {
		t.Run(string(method), func(t *testing.T) {
			f := setup(t, true)
			scope := Scope{Key: f.scope.Key, OwnerID: "alice"}
			f.api.remote.Rights.CanConvertGiftsToStars = true
			f.api.remote.Rights.CanTransferAndUpgradeGifts = true
			if checkErr1 := f.service.Handle(context.Background(), Event{Key: scope.Key, OwnerID: "alice", Generation: "generation", UpdateID: 3, Kind: "connection", Connection: &f.api.remote}); checkErr1 != nil {
				t.Fatal(checkErr1)
			}
			if checkErr2 := f.service.Configure(context.Background(), ConfigureInput{Scope: scope, ExpectedRevision: currentEpoch(t, f), Policy: PeerPolicy{Enabled: true, Consent: true, Mode: ModeConfirm, AllowedMethods: []Method{method}}}); checkErr2 != nil {
				t.Fatal(checkErr2)
			}
			params := Params{"owned_gift_id": json.RawMessage(`"synthetic-owned-gift"`)}
			if method == UpgradeGift {
				params["star_count"] = json.RawMessage(`0`)
			}
			f.service.options.FinancialLimits = FinancialLimits{DailyOperations: 10, DailyStars: 0}
			if _, checkErr3 := f.service.Prepare(context.Background(), PrepareInput{Scope: scope, Method: method, Params: params}); !errors.Is(checkErr3, ErrUnavailable) {
				t.Fatalf("zero budget admitted free financial operation: %v", checkErr3)
			}
			if f.api.calls.Load() != 0 {
				t.Fatal("zero budget issued account mutation")
			}
			// Prove the negative did not merely fail on malformed method parameters.
			f.service.options.FinancialLimits.DailyStars = 1
			if _, checkErr4 := f.service.Prepare(context.Background(), PrepareInput{Scope: scope, Method: method, Params: params}); checkErr4 != nil {
				t.Fatalf("positive budget fixture invalid: %v", checkErr4)
			}
		})
	}
}
