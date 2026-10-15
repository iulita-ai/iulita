// Package transport serves the native mobile gRPC boundary independently from
// Fiber, dashboard JWTs and legacy channel routing. Constructors do not listen.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/mobile"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/ephemeral"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/mobile/securetoken"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Config collects dependencies and limits for the mobile server.
type Config struct {
	TLS      *tls.Config
	OAuth    *oauthstore.Store
	Accounts browseroauth.Accounts
	Chats    *conversations.Repository
	Engine   *engine.Engine
	Devices  *devices.Store
	Features config.MobileConfig
	Version  string
	Ready    func() bool
}

// Server serves the independent mobile protocol.
type Server struct {
	v1.UnimplementedAccountServiceServer
	v1.UnimplementedChatServiceServer
	v1.UnimplementedDeviceServiceServer
	v1.UnimplementedArtifactServiceServer
	v1.UnimplementedPhoneServiceServer
	config  Config
	grpc    *grpc.Server
	slots   chan struct{}
	metrics Counters
	ownerMu sync.Mutex
	owners  map[string]int
	streams map[string]int
}
type principal struct {
	Token  string
	Claims *oauthstore.Claims
}
type authKey struct{}

var errRevoked = errors.New("mobile credential expired or revoked")

// New constructs the subsystem with explicit dependencies and limits.
func New(c Config) (*Server, error) {
	if c.TLS == nil || len(c.TLS.Certificates) == 0 || c.OAuth == nil || c.Accounts == nil || c.Chats == nil || c.Engine == nil {
		return nil, errors.New("mobile TLS/auth/chat dependencies required")
	}
	if err := c.Features.Validate(); err != nil {
		return nil, err
	}
	if c.Features.DeviceEnabled && c.Devices == nil {
		return nil, errors.New("mobile device coordinator required")
	}
	// Native UI/Vision remain gated by joint Android evidence; accepting a
	// server configuration value alone must never open experimental authority.
	if c.Features.UIEnabled && !c.Devices.SupportsUI() || c.Features.VisionEnabled && !c.Devices.SupportsVision() {
		return nil, errors.New("mobile UI and Vision require joint native readiness")
	}
	s := &Server{config: c, slots: make(chan struct{}, 64), owners: map[string]int{}, streams: map[string]int{}}
	tlsConfig := c.TLS.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.NextProtos = []string{"h2"}
	s.grpc = grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.ConnectionTimeout(5*time.Second), grpc.MaxRecvMsgSize(2<<20), grpc.MaxSendMsgSize(2<<20), grpc.MaxHeaderListSize(16<<10), grpc.MaxConcurrentStreams(32), grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: 5 * time.Minute, MaxConnectionAge: 15 * time.Minute, MaxConnectionAgeGrace: 5 * time.Second, Time: time.Minute, Timeout: 10 * time.Second}), grpc.ChainUnaryInterceptor(s.unary), grpc.ChainStreamInterceptor(s.stream))
	v1.RegisterAccountServiceServer(s.grpc, s)
	v1.RegisterChatServiceServer(s.grpc, s)
	v1.RegisterDeviceServiceServer(s.grpc, s)
	v1.RegisterArtifactServiceServer(s.grpc, s)
	v1.RegisterPhoneServiceServer(s.grpc, s)
	return s, nil
}

// Serve serves the configured mobile gRPC listener.
func (s *Server) Serve(listener net.Listener) error { return s.grpc.Serve(listener) }

// Shutdown stops accepting work and drains the mobile server.
func (s *Server) Shutdown(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.grpc.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		s.grpc.Stop()
	}
}
func auth(ctx context.Context) *principal {
	p, ok := ctx.Value(authKey{}).(*principal)
	if !ok {
		return nil
	}
	return p
}

// Called only after the repository has reserved the account DB writer. Lookup
// is a bounded read; taking Accounts.Guard here would deadlock that reservation.
// Sync the current account version before checking the separate OAuth realm so
// password/disable/logout cannot slip between interceptor auth and a DB commit.
func (s *Server) verifyReserved(ctx context.Context, p *principal) error {
	a, err := s.config.Accounts.Lookup(ctx, p.Claims.Subject)
	if err != nil || a.ID != p.Claims.Subject {
		return status.Error(codes.Unauthenticated, "account unavailable")
	}
	available := !a.Disabled && !a.MustChangePassword
	if err = s.config.OAuth.SyncAccount(ctx, a.ID, a.CredentialVersion, available); err != nil || !available {
		return status.Error(codes.Unauthenticated, "account unavailable")
	}
	if _, err = s.config.OAuth.ValidateAccess(ctx, p.Token); err != nil {
		return status.Error(codes.Unauthenticated, "mobile credential no longer valid")
	}
	return nil
}
func (s *Server) validate(ctx context.Context, token string) (*oauthstore.Claims, error) {
	claim, err := s.config.OAuth.VerifySignature(token)
	if err != nil {
		return nil, errRevoked
	}
	err = s.config.Accounts.Guard(ctx, claim.Subject, func(a browseroauth.Account) error {
		available := a.ID == claim.Subject && !a.Disabled && !a.MustChangePassword
		if stepErr2 := s.config.OAuth.SyncAccount(ctx, claim.Subject, a.CredentialVersion, available); stepErr2 != nil {
			return stepErr2
		}
		if !available {
			return errRevoked
		}
		var stepErr3 error
		claim, stepErr3 = s.config.OAuth.ValidateAccess(ctx, token)
		return stepErr3
	})
	if err != nil {
		return nil, errRevoked
	}
	return claim, nil
}
func (s *Server) authorize(ctx context.Context, method string) (*principal, error) {
	if s.config.Ready != nil && !s.config.Ready() {
		return nil, status.Error(codes.Unavailable, "mobile service not ready")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) != 1 || len(values[0]) > 16400 || !strings.HasPrefix(values[0], "Bearer ") {
		return nil, status.Error(codes.Unauthenticated, "mobile bearer credential required")
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" || strings.ContainsAny(token, " \r\n\t") {
		return nil, status.Error(codes.Unauthenticated, "invalid mobile credential")
	}
	c, err := s.validate(ctx, token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "mobile credential expired or revoked")
	}
	p, err := mobile.MethodPolicy(method)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "method unavailable")
	}
	if c.PrincipalType == "service" && ((p.Feature != v1.Feature_FEATURE_CHAT && p.Feature != v1.Feature_FEATURE_IDENTITY) || p.Binding != v1.ResourceBinding_RESOURCE_BINDING_OWNER || p.FreshAuthentication || method == v1.ChatService_RespondToPrompt_FullMethodName || method == v1.ChatService_RespondToConfirmation_FullMethodName) {
		return nil, status.Error(codes.PermissionDenied, "service principal cannot use device or fresh-user authority")
	}
	enabled := false
	switch p.Feature {
	case v1.Feature_FEATURE_IDENTITY:
		enabled = s.config.Features.ChatEnabled || s.config.Features.DeviceEnabled
	case v1.Feature_FEATURE_CHAT:
		enabled = s.config.Features.ChatEnabled
	case v1.Feature_FEATURE_DEVICE:
		enabled = s.config.Features.DeviceEnabled
	case v1.Feature_FEATURE_UI:
		enabled = s.config.Features.UIEnabled
	case v1.Feature_FEATURE_VISION:
		enabled = s.config.Features.VisionEnabled
	}
	if !enabled {
		return nil, mobileError(codes.FailedPrecondition, v1.ErrorReason_ERROR_REASON_FEATURE_DISABLED)
	}
	stage := v1.TokenStage_TOKEN_STAGE_ACTIVE
	switch c.Stage {
	case oauthstore.Pending:
		stage = v1.TokenStage_TOKEN_STAGE_PENDING_ACTIVATION
	case oauthstore.Recovery:
		stage = v1.TokenStage_TOKEN_STAGE_PROVISIONAL_RECOVERY
	}
	if stage != p.Stage {
		return nil, status.Error(codes.PermissionDenied, "credential stage not allowed")
	}
	for _, scope := range p.Scopes {
		if !oauthstore.HasScope(c, scope) {
			return nil, status.Error(codes.PermissionDenied, "mobile scope required")
		}
	}
	if p.FreshAuthentication && !s.config.OAuth.Fresh(c) {
		return nil, mobileError(codes.Unauthenticated, v1.ErrorReason_ERROR_REASON_REAUTHENTICATION_REQUIRED)
	}
	return &principal{token, c}, nil
}
func (s *Server) guardContext(parent context.Context, p *principal) (context.Context, context.CancelFunc) {
	deadline, cancelDeadline := context.WithDeadline(parent, p.Claims.ExpiresAt.Time)
	ctx, cancel := context.WithCancelCause(deadline)
	go func() {
		timer := time.NewTicker(200 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if _, err := s.validate(ctx, p.Token); err != nil {
					cancel(errRevoked)
					return
				}
			}
		}
	}()
	return context.WithValue(ctx, authKey{}, p), func() { cancel(nil); cancelDeadline() }
}
func checkClient(request any) error {
	r, ok := request.(interface{ GetClient() *v1.ClientContext })
	if !ok || r.GetClient() == nil || r.GetClient().GetProtocolVersion() != 1 {
		return mobileError(codes.InvalidArgument, v1.ErrorReason_ERROR_REASON_UNSUPPORTED_PROTOCOL)
	}
	if len(r.GetClient().AppVersion) > 128 || len(r.GetClient().InstallationId) > 128 {
		return status.Error(codes.InvalidArgument, "invalid client context")
	}
	return nil
}
func (s *Server) unary(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, status.Error(codes.ResourceExhausted, "mobile RPC capacity exceeded")
	}
	s.metrics.active.Add(1)
	started := time.Now()
	defer func() { <-s.slots; s.metrics.active.Add(-1); s.metrics.record(status.Code(err), time.Since(started)) }()
	defer func() {
		if recover() != nil {
			response = nil
			err = status.Error(codes.Internal, "mobile request failed")
		}
	}()
	p, err := s.authorize(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	if !s.acquireOwner(p.Claims.Subject, false) {
		return nil, status.Error(codes.ResourceExhausted, "mobile owner capacity exceeded")
	}
	defer s.releaseOwner(p.Claims.Subject, false)
	if checkErr4 := checkClient(request); checkErr4 != nil {
		return nil, checkErr4
	}
	business, cancel := s.guardContext(ctx, p)
	defer cancel()
	business = conversations.WithVerifier(business, func(ctx context.Context) error {
		return s.verifyReserved(ctx, p)
	})
	response, err = handler(business, request)
	if business.Err() != nil && ctx.Err() == nil {
		return nil, status.Error(codes.Unauthenticated, "mobile credential expired or revoked")
	}
	return response, normalize(err)
}

type guardedStream struct {
	grpc.ServerStream
	ctx context.Context
	s   *Server
	p   *principal
}

// Context returns the guarded stream context.
func (s *guardedStream) Context() context.Context { return s.ctx }

// RecvMsg receives a frame while rechecking stream authorization.
func (s *guardedStream) RecvMsg(m any) error {
	done := make(chan error, 1)
	go func() { done <- s.ServerStream.RecvMsg(m) }()
	select {
	case <-s.ctx.Done():
		return status.Error(codes.Unauthenticated, "mobile stream expired or revoked")
	case err := <-done:
		if err != nil {
			return err
		}
		if _, err = s.s.validate(s.ctx, s.p.Token); err != nil {
			return status.Error(codes.Unauthenticated, "mobile stream revoked")
		}
		return nil
	}
}

// SendMsg sends a frame while rechecking stream authorization.
func (s *guardedStream) SendMsg(m any) error {
	if _, err := s.s.validate(s.ctx, s.p.Token); err != nil {
		return status.Error(codes.Unauthenticated, "mobile stream revoked")
	}
	done := make(chan error, 1)
	go func() { done <- s.ServerStream.SendMsg(m) }()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return status.Error(codes.Unauthenticated, "mobile stream expired or revoked")
	case <-timer.C:
		return mobileError(codes.ResourceExhausted, v1.ErrorReason_ERROR_REASON_RESYNC_REQUIRED)
	case err := <-done:
		return err
	}
}
func (s *Server) stream(_ any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	select {
	case s.slots <- struct{}{}:
	default:
		return status.Error(codes.ResourceExhausted, "mobile stream capacity exceeded")
	}
	s.metrics.active.Add(1)
	started := time.Now()
	defer func() { <-s.slots; s.metrics.active.Add(-1); s.metrics.record(status.Code(err), time.Since(started)) }()
	defer func() {
		if recover() != nil {
			err = status.Error(codes.Internal, "mobile stream failed")
		}
	}()
	p, err := s.authorize(stream.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	if !s.acquireOwner(p.Claims.Subject, true) {
		return status.Error(codes.ResourceExhausted, "mobile owner stream capacity exceeded")
	}
	defer s.releaseOwner(p.Claims.Subject, true)
	ctx, cancel := s.guardContext(stream.Context(), p)
	defer cancel()
	ctx = conversations.WithVerifier(ctx, func(ctx context.Context) error {
		return s.verifyReserved(ctx, p)
	})
	err = handler(s, &guardedStream{stream, ctx, s, p})
	if ctx.Err() != nil && stream.Context().Err() == nil {
		return status.Error(codes.Unauthenticated, "mobile stream expired or revoked")
	}
	return normalize(err)
}
func mobileError(code codes.Code, reason v1.ErrorReason) error {
	st := status.New(code, "mobile request rejected")
	detail, err := st.WithDetails(&v1.MobileError{Reason: reason})
	if err != nil {
		return st.Err()
	}
	return detail.Err()
}
func normalize(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, conversations.ErrApproval):
		return status.Error(codes.PermissionDenied, "confirmation scope or fresh authentication required")
	case errors.Is(err, conversations.ErrNotFound), errors.Is(err, oauthstore.ErrNotFound):
		return status.Error(codes.NotFound, "resource not found")
	case errors.Is(err, conversations.ErrIdempotency), errors.Is(err, oauthstore.ErrConflict):
		return mobileError(codes.AlreadyExists, v1.ErrorReason_ERROR_REASON_IDEMPOTENCY_CONFLICT)
	case errors.Is(err, conversations.ErrResync), errors.Is(err, securetoken.ErrCursor):
		return mobileError(codes.FailedPrecondition, v1.ErrorReason_ERROR_REASON_RESYNC_REQUIRED)
	case errors.Is(err, conversations.ErrQueueFull):
		return mobileError(codes.ResourceExhausted, v1.ErrorReason_ERROR_REASON_QUEUE_FULL)
	case errors.Is(err, conversations.ErrConflict), errors.Is(err, conversations.ErrFenced):
		return mobileError(codes.Aborted, v1.ErrorReason_ERROR_REASON_STALE_GENERATION)
	case errors.Is(err, conversations.ErrInvalid), errors.Is(err, oauthstore.ErrInvalid):
		return mobileError(codes.InvalidArgument, v1.ErrorReason_ERROR_REASON_INVALID_INPUT)
	case errors.Is(err, conversations.ErrArchived):
		return status.Error(codes.FailedPrecondition, "conversation archived")
	case errors.Is(err, devices.ErrUnavailable), errors.Is(err, devices.ErrEpoch):
		return mobileError(codes.FailedPrecondition, v1.ErrorReason_ERROR_REASON_DEVICE_UNAVAILABLE)
	case errors.Is(err, devices.ErrBusy):
		return mobileError(codes.ResourceExhausted, v1.ErrorReason_ERROR_REASON_DEVICE_BUSY)
	case errors.Is(err, devices.ErrInvalid):
		return mobileError(codes.InvalidArgument, v1.ErrorReason_ERROR_REASON_INVALID_INPUT)
	case errors.Is(err, ephemeral.ErrExpired):
		return mobileError(codes.NotFound, v1.ErrorReason_ERROR_REASON_RESULT_EXPIRED)
	case errors.Is(err, ephemeral.ErrQuota):
		return status.Error(codes.ResourceExhausted, "ephemeral quota exceeded")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	}
	return status.Error(codes.Internal, "mobile request failed")
}

// mutate replays only a semantic owner/client/method-bound RPC response. No
// external effect may execute inside this account database transaction.
func (s *Server) mutate(ctx context.Context, method string, op *v1.Operation, request, response proto.Message, fn func(context.Context) (proto.Message, error)) (proto.Message, error) {
	if op == nil || op.OperationId == "" || op.IssuedAt == nil || op.IssuedAt.CheckValid() != nil || op.IssuedAt.AsTime().After(time.Now().Add(time.Minute)) || op.IssuedAt.AsTime().Before(time.Now().Add(-7*24*time.Hour)) {
		return nil, conversations.ErrInvalid
	}
	snapshotCopy := proto.Clone(request)
	fields := snapshotCopy.ProtoReflect().Descriptor().Fields()
	if f := fields.ByName("client"); f != nil {
		snapshotCopy.ProtoReflect().Clear(f)
	}
	if f := fields.ByName("operation"); f != nil {
		snapshotCopy.ProtoReflect().Clear(f)
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(snapshotCopy)
	if err != nil {
		return nil, conversations.ErrInvalid
	}
	p := auth(ctx)
	conversation := ""
	if field := fields.ByName("conversation_id"); field != nil {
		conversation = snapshotCopy.ProtoReflect().Get(field).String()
	} else if field := fields.ByName("binding"); field != nil && snapshotCopy.ProtoReflect().Has(field) {
		binding := snapshotCopy.ProtoReflect().Get(field).Message()
		if id := binding.Descriptor().Fields().ByName("conversation_id"); id != nil {
			conversation = binding.Get(id).String()
		}
	}
	result, _, err := s.config.Chats.Operation(ctx, p.Claims.Subject, p.Claims.ClientID, method, op.OperationId, payload, func(ctx context.Context) ([]byte, error) {
		out, stepErr5 := fn(ctx)
		if stepErr5 != nil {
			return nil, stepErr5
		}
		return proto.Marshal(out)
	}, conversation)
	if err != nil {
		return nil, err
	}
	if proto.Unmarshal(result, response) != nil {
		return nil, errors.New("invalid stored mobile response")
	}
	return response, nil
}
