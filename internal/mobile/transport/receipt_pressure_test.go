package transport

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestReceiptUnderValidationPressure(t *testing.T) {
	h := setupHarness(t)
	token, device := h.login(t, "owner")
	n := startNative(t, h, token, &v1.SessionMirror{SessionId: uuid.NewString(), SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), PolicyRevision: devices.PolicyRevision})
	origin(t, h, token, "phone-status:"+device)
	command := n.command(t)
	ctx, cancel := context.WithCancel(tokenContext(token))
	var completed atomic.Int64
	faults := make(chan error, 1)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for ctx.Err() == nil {
				reply, err := v1.NewAccountServiceClient(h.conn).GetProfile(ctx, &v1.GetProfileRequest{Client: clientContext()})
				if err == nil && reply.OwnerId == h.owners["owner"] {
					completed.Add(1)
					continue
				}
				if ctx.Err() != nil || status.Code(err) == codes.ResourceExhausted {
					continue
				}
				select {
				case faults <- fmt.Errorf("profile pressure request failed: %w", err):
				default:
				}
				return
			}
		}()
	}
	close(start)
	time.Sleep(200 * time.Millisecond)
	n.receipt(t, command, v1.ReceiptState_RECEIPT_STATE_STARTED)
	cancel()
	wg.Wait()
	select {
	case err := <-faults:
		t.Fatal(err)
	default:
	}
	if completed.Load() == 0 {
		t.Fatal("validation pressure never completed a real authenticated request")
	}
}
