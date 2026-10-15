package transport

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/google/uuid"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) serverInfo() *v1.ServerInfo {
	out := &v1.ServerInfo{ProtocolVersion: 1, ServerVersion: s.config.Version, MaxTextBytes: 1 << 20, MaxPageSize: 100, MaxChunkBytes: 64 << 10, AvailableFeatures: []v1.Feature{v1.Feature_FEATURE_IDENTITY}}
	if s.config.Features.ChatEnabled {
		out.AvailableFeatures = append(out.AvailableFeatures, v1.Feature_FEATURE_CHAT)
	}
	if s.config.Features.DeviceEnabled {
		out.AvailableFeatures = append(out.AvailableFeatures, v1.Feature_FEATURE_DEVICE)
	}
	return out
}

// GetProfile implements the authenticated GetProfile mobile RPC.
func (s *Server) GetProfile(ctx context.Context, _ *v1.GetProfileRequest) (*v1.GetProfileResponse, error) {
	a, err := s.config.Accounts.Lookup(ctx, auth(ctx).Claims.Subject)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "account unavailable")
	}
	return &v1.GetProfileResponse{OwnerId: a.ID, DisplayName: a.DisplayName, Server: s.serverInfo()}, nil
}
func wireDevice(d oauthstore.Device) *v1.Device {
	state := v1.DeviceState_DEVICE_STATE_PENDING
	if d.Activated {
		state = v1.DeviceState_DEVICE_STATE_ACTIVE
	}
	if d.Revoked {
		state = v1.DeviceState_DEVICE_STATE_REVOKED
	} else if !d.Activated && d.PendingExpiresAt <= time.Now().Unix() {
		state = v1.DeviceState_DEVICE_STATE_EXPIRED
	}
	return &v1.Device{DeviceId: d.ID, Label: d.Label, State: state, CredentialEpoch: d.Epoch}
}
func (s *Server) withIdentity(ctx context.Context, fn func(*oauthstore.Claims) error) error {
	p := auth(ctx)
	return s.config.Accounts.Guard(ctx, p.Claims.Subject, func(a browseroauth.Account) error {
		available := a.ID == p.Claims.Subject && !a.Disabled && !a.MustChangePassword
		if err := s.config.OAuth.SyncAccount(ctx, p.Claims.Subject, a.CredentialVersion, available); err != nil {
			return err
		}
		if !available {
			return status.Error(codes.Unauthenticated, "account unavailable")
		}
		c, err := s.config.OAuth.ValidateAccess(ctx, p.Token)
		if err != nil {
			return status.Error(codes.Unauthenticated, "credential no longer valid")
		}
		return fn(c)
	})
}
func validOperation(op *v1.Operation) bool {
	if op == nil || op.IssuedAt == nil || op.IssuedAt.CheckValid() != nil || op.IssuedAt.AsTime().After(time.Now().Add(time.Minute)) || op.IssuedAt.AsTime().Before(time.Now().Add(-7*24*time.Hour)) {
		return false
	}
	id, err := uuid.Parse(op.OperationId)
	return err == nil && id != uuid.Nil && id.String() == op.OperationId
}

// ActivateDevice implements the authenticated ActivateDevice mobile RPC.
func (s *Server) ActivateDevice(ctx context.Context, in *v1.ActivateDeviceRequest) (*v1.Device, error) {
	if !validOperation(in.Operation) || in.PublicKey == nil || in.Proof == nil || len(in.PublicKey.P256X) != 32 || len(in.PublicKey.P256Y) != 32 {
		return nil, oauthstore.ErrInvalid
	}
	j := oauthstore.InstallationJWK{Crv: "P-256", Kty: "EC", X: base64.RawURLEncoding.EncodeToString(in.PublicKey.P256X), Y: base64.RawURLEncoding.EncodeToString(in.PublicKey.P256Y)}
	var result oauthstore.Device
	err := s.withIdentity(ctx, func(c *oauthstore.Claims) error {
		var err error
		result, err = s.config.OAuth.FinishInstallation(ctx, c, in.Operation.OperationId, in.Proof.Nonce, in.Proof.SignatureDer, &j, in.Label)
		return err
	})
	if err != nil {
		return nil, err
	}
	result, err = s.config.OAuth.Device(ctx, result.OwnerID, result.ID)
	return wireDevice(result), err
}

// CompleteRecovery implements the authenticated CompleteRecovery mobile RPC.
func (s *Server) CompleteRecovery(ctx context.Context, in *v1.CompleteRecoveryRequest) (*v1.Device, error) {
	if !validOperation(in.Operation) || in.Proof == nil {
		return nil, oauthstore.ErrInvalid
	}
	var result oauthstore.Device
	err := s.withIdentity(ctx, func(c *oauthstore.Claims) error {
		if !s.config.OAuth.Fresh(c) {
			return status.Error(codes.Unauthenticated, "fresh password verification required")
		}
		var err error
		result, err = s.config.OAuth.FinishInstallation(ctx, c, in.Operation.OperationId, in.Proof.Nonce, in.Proof.SignatureDer, nil, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	result, err = s.config.OAuth.Device(ctx, result.OwnerID, result.ID)
	return wireDevice(result), err
}

// ListDevices implements the authenticated ListDevices mobile RPC.
func (s *Server) ListDevices(ctx context.Context, in *v1.ListDevicesRequest) (*v1.ListDevicesResponse, error) {
	devices, next, err := s.config.OAuth.ListDevices(ctx, auth(ctx).Claims.Subject, pageSize(in.PageSize), in.PageToken)
	if err != nil {
		return nil, err
	}
	out := &v1.ListDevicesResponse{NextPageToken: next}
	for _, d := range devices {
		out.Devices = append(out.Devices, wireDevice(d))
	}
	return out, nil
}

// RevokeDevice implements the authenticated RevokeDevice mobile RPC.
func (s *Server) RevokeDevice(ctx context.Context, in *v1.RevokeDeviceRequest) (*v1.Device, error) {
	if !validOperation(in.Operation) {
		return nil, oauthstore.ErrInvalid
	}
	var result oauthstore.Device
	err := s.withIdentity(ctx, func(c *oauthstore.Claims) error {
		if !s.config.OAuth.Fresh(c) {
			return status.Error(codes.Unauthenticated, "fresh password verification required")
		}
		if err := s.config.OAuth.RevokeDeviceOperation(ctx, c.Subject, c.ClientID, in.Operation.OperationId, in.DeviceId); err != nil {
			return err
		}
		var err error
		result, err = s.config.OAuth.Device(ctx, c.Subject, in.DeviceId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return wireDevice(result), nil
}
