package transport

import (
	"context"
	"errors"
	"io"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/httpartifact"
	"github.com/iulita-ai/iulita/internal/httpfetch"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
)

func (s *Server) httpArtifactContext(ctx context.Context, chat string, generation uint64) (context.Context, error) {
	if s.config.HTTPArtifacts == nil || generation == 0 {
		return ctx, conversations.ErrNotFound
	}
	p := auth(ctx)
	if p.Claims.PrincipalType == "service" {
		return ctx, conversations.ErrNotFound
	}
	c, err := s.config.Chats.Get(ctx, p.Claims.Subject, chat)
	if err != nil {
		return ctx, err
	}
	if c.Generation != generation {
		return ctx, conversations.ErrConflict
	}
	return httpfetch.WithScope(ctx, httpfetch.Scope{UserID: p.Claims.Subject, NativeConversation: chat, NativeGeneration: generation}), nil
}
func httpArtifactInfo(a *domain.HTTPArtifact) *v1.HTTPArtifactInfo {
	return &v1.HTTPArtifactInfo{ArtifactId: a.ID, Filename: a.Filename, Mime: a.MIME, Size: uint64(max(a.Size, 0)), Sha256: a.SHA256}
}

// ListHTTPArtifacts lists ordinary HTTP attachments for the owned chat generation.
func (s *Server) ListHTTPArtifacts(ctx context.Context, in *v1.ListHTTPArtifactsRequest) (*v1.ListHTTPArtifactsResponse, error) {
	ctx, err := s.httpArtifactContext(ctx, in.ConversationId, in.HistoryGeneration)
	if err != nil {
		return nil, err
	}
	scope, _ := httpfetch.ScopeFrom(ctx)
	items, err := s.config.HTTPArtifacts.List(ctx, scope.UserID)
	if err != nil {
		return nil, err
	}
	out := &v1.ListHTTPArtifactsResponse{}
	for n := range items {
		a := &items[n]
		if a.ChatID == scope.ArtifactBinding() && a.State == domain.HTTPArtifactReady {
			out.Artifacts = append(out.Artifacts, httpArtifactInfo(a))
		}
	}
	return out, nil
}

// ReadHTTPArtifact reads a bounded range while rechecking revocation and generation.
func (s *Server) ReadHTTPArtifact(ctx context.Context, in *v1.ReadHTTPArtifactRequest) (*v1.HTTPArtifactChunk, error) {
	if in.MaxBytes == 0 || in.MaxBytes > 64<<10 || in.Offset > 1<<40 {
		return nil, conversations.ErrInvalid
	}
	ctx, err := s.httpArtifactContext(ctx, in.ConversationId, in.HistoryGeneration)
	if err != nil {
		return nil, err
	}
	scope, _ := httpfetch.ScopeFrom(ctx)
	a, err := s.config.HTTPArtifacts.Get(ctx, scope.UserID, in.ArtifactId)
	if err != nil || a.ChatID != scope.ArtifactBinding() || a.State != domain.HTTPArtifactReady {
		return nil, conversations.ErrNotFound
	}
	if in.Offset > uint64(max(a.Size, 0)) {
		return nil, conversations.ErrInvalid
	}
	reader, err := s.config.HTTPArtifacts.Open(ctx, scope.UserID, in.ArtifactId)
	if err != nil {
		return nil, conversations.ErrNotFound
	}
	defer reader.Close() //nolint:errcheck // Read-only cleanup.
	seeker, ok := reader.(io.Seeker)
	if !ok {
		return nil, conversations.ErrNotFound
	}
	if _, err = seeker.Seek(int64(in.Offset), io.SeekStart); err != nil {
		return nil, err
	}
	n := min(uint64(in.MaxBytes), uint64(max(a.Size, 0))-in.Offset)
	body := make([]byte, n)
	if _, err = io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	// No bytes escape a concurrently cleared chat or revoked credential.
	if checkErr := s.verifyReserved(ctx, auth(ctx)); checkErr != nil {
		return nil, checkErr
	}
	if _, err = s.httpArtifactContext(ctx, in.ConversationId, in.HistoryGeneration); err != nil {
		return nil, err
	}
	return &v1.HTTPArtifactChunk{Info: httpArtifactInfo(a), Offset: in.Offset, Chunk: body, Eof: in.Offset+n == uint64(max(a.Size, 0))}, nil
}

// DeleteHTTPArtifact idempotently deletes an owned ordinary HTTP attachment.
func (s *Server) DeleteHTTPArtifact(ctx context.Context, in *v1.DeleteHTTPArtifactRequest) (*v1.HTTPArtifactDeleted, error) {
	ctx, err := s.httpArtifactContext(ctx, in.ConversationId, in.HistoryGeneration)
	if err != nil {
		return nil, err
	}
	scope, _ := httpfetch.ScopeFrom(ctx)
	a, err := s.config.HTTPArtifacts.Get(ctx, scope.UserID, in.ArtifactId)
	if errors.Is(err, httpartifact.ErrNotFound) {
		return &v1.HTTPArtifactDeleted{}, nil
	}
	if err != nil {
		return nil, err
	}
	if a.ChatID != scope.ArtifactBinding() {
		return nil, conversations.ErrNotFound
	}
	if checkErr := s.verifyReserved(ctx, auth(ctx)); checkErr != nil {
		return nil, checkErr
	}
	if err = s.config.HTTPArtifacts.Delete(ctx, scope.UserID, in.ArtifactId); err != nil && !errors.Is(err, httpartifact.ErrNotFound) {
		return nil, err
	}
	return &v1.HTTPArtifactDeleted{}, nil
}
