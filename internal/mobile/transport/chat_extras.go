package transport

import (
	"context"
	"sort"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// GetChatCapabilities never substitutes account/admin credentials for the
// caller's scopes. Availability can shrink after this advisory read; admission
// and dispatch independently recheck live policy.
func (s *Server) GetChatCapabilities(ctx context.Context, in *v1.GetChatCapabilitiesRequest) (*v1.GetChatCapabilitiesResponse, error) {
	p := auth(ctx)
	a := s.config.OAuth.Authority(p.Claims)
	c, err := s.config.Chats.Get(ctx, p.Claims.Subject, in.ConversationId)
	if err != nil {
		return nil, err
	}
	tools, err := s.config.Engine.Capabilities(engine.WithAuthority(ctx, a), p.Claims.Subject, c)
	if err != nil {
		return nil, err
	}
	out := &v1.GetChatCapabilitiesResponse{ConversationVersion: c.Version, MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}
	if c.Settings.MemoryMode == "account" {
		out.MemoryMode = v1.MemoryMode_MEMORY_MODE_ACCOUNT
	}
	for i := range tools {
		t := tools[i]
		item := &v1.ChatToolCapability{InternalConfirmations: t.InternalConfirmations, Name: t.Definition.Name, Description: t.Definition.Description, PolicyRevision: t.Revision}
		if len(t.Actions) == 0 {
			item.Actions = append(item.Actions, &v1.ChatActionCapability{Effect: t.Effect})
		}
		for name, action := range t.Actions {
			allowed := true
			for _, scope := range action.Scopes {
				found := false
				for _, granted := range a.Scopes {
					if scope == granted {
						found = true
						break
					}
				}
				if !found {
					allowed = false
					break
				}
			}
			if allowed {
				item.Actions = append(item.Actions, &v1.ChatActionCapability{Action: name, Effect: action.Effect, ConfirmationRequired: action.Confirmation})
			}
		}
		sort.Slice(item.Actions, func(i, j int) bool { return item.Actions[i].Action < item.Actions[j].Action })
		out.Tools = append(out.Tools, item)
	}
	sort.Slice(out.Tools, func(i, j int) bool { return out.Tools[i].Name < out.Tools[j].Name })
	return out, nil
}

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
