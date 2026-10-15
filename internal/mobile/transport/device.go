package transport

import (
	"context"
	"strings"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func identity(ctx context.Context) devices.Identity {
	p := auth(ctx)
	return devices.Identity{Token: p.Token, Claims: p.Claims}
}

// Control implements the authenticated Control mobile RPC.
func (s *Server) Control(stream grpc.BidiStreamingServer[v1.ClientControlFrame, v1.ServerControlFrame]) error {
	ctx := stream.Context()
	p := auth(ctx)
	type helloResult struct {
		frame *v1.ClientControlFrame
		err   error
	}
	hello := make(chan helloResult, 1)
	go func() { frame, err := stream.Recv(); hello <- helloResult{frame, err} }()
	var first *v1.ClientControlFrame
	var err error
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case result := <-hello:
		first, err = result.frame, result.err
	case <-timer.C:
		return status.Error(codes.DeadlineExceeded, "control hello deadline exceeded")
	case <-ctx.Done():
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if first == nil || first.ProtocolVersion != 1 || first.ControlStreamEpoch != 0 || first.GetHello() == nil || first.GetHello().Client == nil || first.GetHello().Client.ProtocolVersion != 1 {
		return status.Error(codes.InvalidArgument, "control hello required")
	}
	epoch, seq, err := s.config.Devices.OpenControl(ctx, identity(ctx))
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = s.config.Devices.CloseControl(cleanup, p.Claims.Subject, p.Claims.DeviceID, epoch) //nolint:errcheck // The bounded control lease expires even if disconnect cleanup fails.
	}()
	send := func(frame *v1.ServerControlFrame) error {
		frame.ProtocolVersion = 1
		frame.ControlStreamEpoch = epoch
		scopes, stepErr1 := mobile.ServerFrameScopes(frame)
		if stepErr1 != nil {
			return stepErr1
		}
		for _, scope := range scopes {
			if !oauthstore.HasScope(p.Claims, scope) {
				return status.Error(codes.PermissionDenied, "frame scope required")
			}
		}
		return stream.Send(frame)
	}
	if checkErr3 := send(&v1.ServerControlFrame{Body: &v1.ServerControlFrame_Welcome{Welcome: &v1.ControlWelcome{ControlStreamEpoch: epoch, CurrentServerSeq: seq, Server: s.serverInfo()}}}); checkErr3 != nil {
		return checkErr3
	}
	type received struct {
		frame *v1.ClientControlFrame
		err   error
	}
	incoming := make(chan received, 8)
	go func() {
		for {
			frame, stepErr2 := stream.Recv()
			select {
			case incoming <- received{frame, stepErr2}:
			case <-ctx.Done():
				return
			}
			if stepErr2 != nil {
				return
			}
		}
	}()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(3 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat.C:
			if checkErr4 := s.config.Devices.TouchControl(ctx, identity(ctx), epoch); checkErr4 != nil {
				return checkErr4
			}
		case <-poll.C:
			revoked, err := s.config.Devices.ReconcileParents(ctx, identity(ctx), epoch)
			if err != nil {
				return err
			}
			for _, revocation := range revoked {
				if checkErr5 := send(&v1.ServerControlFrame{Body: &v1.ServerControlFrame_Revoked{Revoked: revocation}}); checkErr5 != nil {
					return checkErr5
				}
			}
			command, err := s.config.Devices.Next(ctx, identity(ctx), epoch)
			if err != nil {
				return err
			}
			if command != nil {
				if command.GetUi() != nil {
					lease, leaseErr := s.config.Devices.UILease(ctx, identity(ctx), epoch, &v1.LeaseRenewal{TaskId: command.TaskId, SessionId: command.SessionId, SessionGeneration: command.SessionGeneration})
					err = leaseErr
					if err == nil {
						err = send(&v1.ServerControlFrame{Body: &v1.ServerControlFrame_Lease{Lease: lease}})
					}
				}
				if err == nil {
					err = send(&v1.ServerControlFrame{Body: &v1.ServerControlFrame_Command{Command: command}})
				}
				if err != nil {
					cleanup, c := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
					if dispatchErr := s.config.Devices.DispatchLost(cleanup, p.Claims.Subject, p.Claims.DeviceID, command.CommandId); dispatchErr != nil {
						c()
						return dispatchErr
					}
					c()
					return err
				}
			}
		case in := <-incoming:
			if in.err != nil {
				return in.err
			}
			frame := in.frame
			if frame == nil || frame.ControlStreamEpoch != epoch {
				return devices.ErrEpoch
			}
			scopes, err := mobile.ClientFrameScopes(frame)
			if err != nil {
				return status.Error(codes.InvalidArgument, "invalid control frame")
			}
			for _, scope := range scopes {
				if !oauthstore.HasScope(p.Claims, scope) {
					return status.Error(codes.PermissionDenied, "frame scope required")
				}
			}
			switch body := frame.Body.(type) {
			case *v1.ClientControlFrame_Capabilities:
				err = s.config.Devices.Capabilities(ctx, identity(ctx), epoch, body.Capabilities)
			case *v1.ClientControlFrame_Session:
				err = s.config.Devices.Session(ctx, identity(ctx), epoch, body.Session)
			case *v1.ClientControlFrame_Renew:
				if !s.config.Features.UIEnabled {
					return devices.ErrUnavailable
				}
				lease, renewErr := s.config.Devices.RenewUI(ctx, identity(ctx), epoch, body.Renew)
				err = renewErr
				if err == nil {
					err = send(&v1.ServerControlFrame{Body: &v1.ServerControlFrame_Lease{Lease: lease}})
				}
			case *v1.ClientControlFrame_Receipt:
				err = s.config.Devices.Receipt(ctx, identity(ctx), epoch, body.Receipt)
				if err == nil {
					err = send(&v1.ServerControlFrame{Body: &v1.ServerControlFrame_ReceiptAck{ReceiptAck: &v1.ReceiptAck{ReceiptId: body.Receipt.ReceiptId}}})
				}
			default:
				return mobileError(codes.FailedPrecondition, v1.ErrorReason_ERROR_REASON_FEATURE_DISABLED)
			}
			if err != nil {
				return err
			}
		}
	}
}
func wireTask(t devices.Task) *v1.Task {
	state := v1.TaskState(v1.TaskState_value["TASK_STATE_"+strings.ToUpper(t.State)])
	return &v1.Task{TaskId: t.ID, DeviceId: t.Device, Origin: &v1.TaskOrigin{ChannelType: "mobile", ChannelInstance: "mobile", BindingRevision: devices.PolicyRevision, ConversationId: t.Conversation, TurnId: t.Turn}, State: state, CancellationRequested: t.Cancelled, OutcomeReason: t.Reason, ExpiresAt: timestamppb.New(t.ExpiresAt), CurrentStep: max(1, t.CurrentStep), ResultDestinationDeviceId: t.ViewerDevice}
}

// GetTask implements the authenticated GetTask mobile RPC.
func (s *Server) GetTask(ctx context.Context, in *v1.GetTaskRequest) (*v1.Task, error) {
	t, err := s.config.Devices.GetTask(ctx, identity(ctx), in.TaskId)
	if err != nil {
		return nil, err
	}
	return wireTask(t), nil
}

// GetTaskResult implements the authenticated GetTaskResult mobile RPC.
func (s *Server) GetTaskResult(ctx context.Context, in *v1.GetTaskResultRequest) (*v1.EphemeralResult, error) {
	return s.config.Devices.Result(ctx, identity(ctx), in.TaskId)
}

// SyncObservations implements the authenticated SyncObservations mobile RPC.
func (s *Server) SyncObservations(ctx context.Context, in *v1.SyncObservationsRequest) (*v1.SyncObservationsResponse, error) {
	if !validOperation(in.Operation) {
		return nil, devices.ErrInvalid
	}
	ids, err := s.config.Devices.Sync(ctx, identity(ctx), in.Operation.OperationId, in.Observations)
	if err != nil {
		return nil, err
	}
	return &v1.SyncObservationsResponse{AcceptedObservationIds: ids}, nil
}
