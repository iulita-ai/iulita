package transport

import (
	"context"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
)

// CreateUITask requires both caller approval and an exact native envelope grant.
func (s *Server) CreateUITask(ctx context.Context, in *v1.CreateUITaskRequest) (*v1.Task, error) {
	if in == nil || !validOperation(in.Operation) {
		return nil, devices.ErrInvalid
	}
	task, err := s.config.Devices.CreateUI(ctx, identity(ctx), in)
	if err != nil {
		return nil, err
	}
	return wireTask(task), nil
}

// PrepareProcessing discloses the pinned recipient before any phone-data transfer.
func (s *Server) PrepareProcessing(ctx context.Context, in *v1.PrepareProcessingRequest) (*v1.ProcessingOffer, error) {
	if in == nil || !validOperation(in.Operation) {
		return nil, devices.ErrInvalid
	}
	if in.Purpose == v1.ProcessingPurpose_PROCESSING_PURPOSE_UI_PLAN && !s.config.Features.UIEnabled {
		return nil, devices.ErrUnavailable
	}
	return s.config.Devices.PrepareProcessing(ctx, identity(ctx), in)
}

// ProcessResult performs a single explicitly approved private provider attempt.
func (s *Server) ProcessResult(ctx context.Context, in *v1.ProcessResultRequest) (*v1.EphemeralResult, error) {
	if in == nil || !validOperation(in.Operation) {
		return nil, devices.ErrInvalid
	}
	return s.config.Devices.ProcessResult(ctx, identity(ctx), in)
}

// BindExternalOrigin binds one current canonical private chat to an owned device.
func (s *Server) BindExternalOrigin(ctx context.Context, in *v1.BindExternalOriginRequest) (*v1.ExternalOriginBinding, error) {
	if in == nil || !validOperation(in.Operation) {
		return nil, devices.ErrInvalid
	}
	return s.config.Devices.BindExternal(ctx, identity(ctx), in)
}

// RevokeExternalOrigin closes future authority rooted in a private-chat link.
func (s *Server) RevokeExternalOrigin(ctx context.Context, in *v1.RevokeExternalOriginRequest) (*v1.ExternalOriginBinding, error) {
	if in == nil || !validOperation(in.Operation) {
		return nil, devices.ErrInvalid
	}
	return s.config.Devices.RevokeExternal(ctx, identity(ctx), in)
}

// Upload accepts only ephemeral images bound to an approved native window capture.
func (s *Server) Upload(ctx context.Context, in *v1.UploadArtifactRequest) (*v1.Artifact, error) {
	if in == nil || !validOperation(in.Operation) {
		return nil, devices.ErrInvalid
	}
	return s.config.Devices.UploadArtifact(ctx, identity(ctx), in)
}

// Read returns only bounded artifact bytes to the currently authorized viewer.
func (s *Server) Read(ctx context.Context, in *v1.ReadArtifactRequest) (*v1.ArtifactChunk, error) {
	return s.config.Devices.ReadArtifact(ctx, identity(ctx), in)
}
