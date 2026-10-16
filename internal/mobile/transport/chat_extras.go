package transport

import (
	"context"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ListChatProfiles implements the authenticated owned chat extension RPC.
func (s *Server) ListChatProfiles(ctx context.Context, in *v1.ListChatProfilesRequest) (*v1.ListChatProfilesResponse, error) {
	out := &v1.ListChatProfilesResponse{}
	if s.config.Engine.Profiles != nil {
		for _, p := range s.config.Engine.Profiles() {
			out.Profiles = append(out.Profiles, &v1.ChatProfile{ProfileId: p.ID, Images: p.Images})
		}
	}
	return out, nil
}

// SendPhotos implements the authenticated owned chat extension RPC.
func (s *Server) SendPhotos(ctx context.Context, in *v1.SendPhotosRequest) (*v1.Turn, error) {
	data := make([][]byte, len(in.Photos))
	for i, p := range in.Photos {
		data[i] = p.GetJpeg()
	}
	photos, err := conversations.CanonicalPhotos(ctx, data)
	if err != nil {
		return nil, err
	}
	out, err := s.mutate(ctx, "SendPhotos", in.Operation, in, new(v1.Turn), func(ctx context.Context) (proto.Message, error) {
		p := auth(ctx)
		t, _, admissionErr := s.config.Engine.Accept(engine.WithAuthority(ctx, s.config.OAuth.Authority(p.Claims)), p.Claims.Subject, in.ConversationId, conversations.Input{ClientMessageID: in.ClientMessageId, Generation: in.ExpectedHistoryGeneration, Text: in.Text, Photos: photos})
		return wireTurn(t), admissionErr
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Turn](out)
}
func wirePhoto(p conversations.PhotoInfo) *v1.ChatPhotoInfo {
	return &v1.ChatPhotoInfo{PhotoId: p.ID, MessageId: p.MessageID, Position: uint32(p.Position), Width: uint32(p.Width), Height: uint32(p.Height), Size: uint32(p.Size)} //nolint:gosec // Store validates position0..3, dimensions1..2048 and size1..384KiB.
}

// ListChatPhotos implements the authenticated owned chat extension RPC.
func (s *Server) ListChatPhotos(ctx context.Context, in *v1.ListChatPhotosRequest) (*v1.ListChatPhotosResponse, error) {
	photos, err := s.config.Chats.ListPhotos(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.HistoryGeneration, in.MessageIds)
	if err != nil {
		return nil, err
	}
	out := &v1.ListChatPhotosResponse{}
	for _, p := range photos {
		out.Photos = append(out.Photos, wirePhoto(p))
	}
	return out, nil
}

// GetChatPhoto implements the authenticated owned chat extension RPC.
func (s *Server) GetChatPhoto(ctx context.Context, in *v1.GetChatPhotoRequest) (*v1.ChatPhoto, error) {
	p, data, err := s.config.Chats.GetPhoto(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.HistoryGeneration, in.PhotoId)
	if err != nil {
		return nil, err
	}
	return &v1.ChatPhoto{Info: wirePhoto(p), Jpeg: data}, nil
}

// SaveAnswerFact implements the authenticated owned chat extension RPC.
func (s *Server) SaveAnswerFact(ctx context.Context, in *v1.SaveAnswerFactRequest) (*v1.SavedAnswerFact, error) {
	if auth(ctx).Claims.PrincipalType == "service" || !in.ConfirmAccountExport {
		return nil, status.Error(codes.PermissionDenied, "explicit interactive account export required")
	}
	out, err := s.mutate(ctx, "SaveAnswerFact", in.Operation, in, new(v1.SavedAnswerFact), func(ctx context.Context) (proto.Message, error) {
		id, err := s.config.Chats.SaveAnswerFact(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.HistoryGeneration, in.MessageId, in.Text)
		return &v1.SavedAnswerFact{FactId: id}, err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.SavedAnswerFact](out)
}
