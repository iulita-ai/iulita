package transport

import (
	"context"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
)

// UploadBatch accepts immutable passive history under token-device authority.
func (s *Server) UploadBatch(ctx context.Context, in *v1.UploadTelemetryRequest) (*v1.UploadTelemetryResponse, error) {
	ids, err := s.config.Devices.UploadTelemetry(ctx, identity(ctx), in.Records)
	if err != nil {
		return nil, err
	}
	return &v1.UploadTelemetryResponse{AcceptedRecordIds: ids, ServerClockId: s.config.Clock.ID()}, nil
}

// ListHistory returns bounded private history without chat or model processing.
func (s *Server) ListHistory(ctx context.Context, in *v1.ListTelemetryRequest) (*v1.ListTelemetryResponse, error) {
	since := time.Now().Add(-24 * time.Hour)
	if in.Since != nil {
		if in.Since.CheckValid() != nil {
			return nil, devices.ErrInvalid
		}
		since = in.Since.AsTime()
	}
	limit := int(in.PageSize)
	if limit == 0 {
		limit = 100
	}
	records, next, err := s.config.Devices.ListTelemetry(ctx, identity(ctx), since, in.BeforeRecordId, limit)
	if err != nil {
		return nil, err
	}
	return &v1.ListTelemetryResponse{Records: records, NextRecordId: next, ServerClockId: s.config.Clock.ID()}, nil
}
