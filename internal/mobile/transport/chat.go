package transport

import (
	"context"
	"strings"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func settings(s *v1.ConversationSettings) (conversations.Settings, error) {
	if s == nil {
		return conversations.Settings{}, conversations.ErrInvalid
	}
	mode := ""
	switch s.MemoryMode {
	case v1.MemoryMode_MEMORY_MODE_ISOLATED:
		mode = "isolated"
	case v1.MemoryMode_MEMORY_MODE_ACCOUNT:
		mode = "account"
	default:
		return conversations.Settings{}, conversations.ErrInvalid
	}
	return conversations.Settings{ProfileID: s.ProfileId, MemoryMode: mode}, nil
}
func wireSettings(s conversations.Settings) *v1.ConversationSettings {
	mode := v1.MemoryMode_MEMORY_MODE_ISOLATED
	if s.MemoryMode == "account" {
		mode = v1.MemoryMode_MEMORY_MODE_ACCOUNT
	}
	return &v1.ConversationSettings{ProfileId: s.ProfileID, MemoryMode: mode}
}
func wireConversation(c conversations.Conversation) *v1.Conversation {
	return &v1.Conversation{ConversationId: c.ID, Title: c.Title, Settings: wireSettings(c.Settings), Version: c.Version, HistoryGeneration: c.Generation, Archived: c.Archived, CreatedAt: timestamppb.New(c.CreatedAt), UpdatedAt: timestamppb.New(c.UpdatedAt), ReadPosition: &v1.ReadPosition{ConversationId: c.ID, HistoryGeneration: c.Generation, MessageSeq: c.ReadSeq}, UnreadCount: c.UnreadCount}
}
func wireTurn(t conversations.Turn) *v1.Turn {
	state := v1.TurnState(v1.TurnState_value["TURN_STATE_"+strings.ToUpper(t.State)])
	out := &v1.Turn{TurnId: t.ID, ConversationId: t.ConversationID, ClientMessageId: t.ClientMessageID, TurnSeq: t.TurnSeq, HistoryGeneration: t.Generation, State: state, CancellationRequested: t.CancellationRequested, InputMessageId: t.InputMessageID, OutputMessageId: t.OutputMessageID, AcceptedSettings: wireSettings(t.AcceptedSettings)}
	if !t.CancellationRequestedAt.IsZero() {
		out.CancellationRequestedAt = timestamppb.New(t.CancellationRequestedAt)
	}
	if t.State == "unknown_outcome" {
		out.OutcomeReason = v1.ErrorReason_ERROR_REASON_UNKNOWN_OUTCOME
	}
	return out
}
func wirePrompt(p conversations.Prompt) *v1.Prompt {
	state := v1.PromptState(v1.PromptState_value["PROMPT_STATE_"+strings.ToUpper(p.State)])
	out := &v1.Prompt{Binding: &v1.PromptBinding{ConversationId: p.ConversationID, TurnId: p.TurnID, PromptId: p.ID, HistoryGeneration: p.Generation}, State: state, ExpiresAt: timestamppb.New(p.ExpiresAt)}
	if p.Kind == "confirmation" {
		out.Body = &v1.Prompt_Confirmation{Confirmation: &v1.SecurityConfirmation{OperationId: p.OperationID, Nonce: p.Nonce, FingerprintSha256: p.Fingerprint, SafePreview: p.Preview, ExpiresAt: timestamppb.New(p.ExpiresAt)}}
	} else {
		choice := &v1.ChoicePrompt{Question: p.Question}
		for _, c := range p.Choices {
			choice.Choices = append(choice.Choices, &v1.Choice{ChoiceId: c.ID, Label: c.Label})
		}
		out.Body = &v1.Prompt_Choice{Choice: choice}
	}
	return out
}
func pageSize(n uint32) int {
	if n == 0 {
		return 50
	}
	if n > 100 {
		return 101
	}
	return int(n)
}

// CreateConversation implements the authenticated CreateConversation mobile RPC.
func (s *Server) CreateConversation(ctx context.Context, in *v1.CreateConversationRequest) (*v1.Conversation, error) {
	out, err := s.mutate(ctx, "CreateConversation", in.Operation, in, new(v1.Conversation), func(ctx context.Context) (proto.Message, error) {
		set, err := settings(in.Settings)
		if err != nil {
			return nil, err
		}
		if _, err = s.config.Engine.Resolve(ctx, auth(ctx).Claims.Subject, set.ProfileID); err != nil {
			return nil, conversations.ErrInvalid
		}
		c, err := s.config.Chats.Create(ctx, auth(ctx).Claims.Subject, in.Title, set)
		return wireConversation(c), err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Conversation](out)
}

// ListConversations implements the authenticated ListConversations mobile RPC.
func (s *Server) ListConversations(ctx context.Context, in *v1.ListConversationsRequest) (*v1.ListConversationsResponse, error) {
	items, next, err := s.config.Chats.List(ctx, auth(ctx).Claims.Subject, in.IncludeArchived, pageSize(in.PageSize), in.PageToken)
	if err != nil {
		return nil, err
	}
	out := &v1.ListConversationsResponse{NextPageToken: next}
	for i := range items {
		out.Conversations = append(out.Conversations, wireConversation(items[i]))
	}
	return out, nil
}

// GetConversation implements the authenticated GetConversation mobile RPC.
func (s *Server) GetConversation(ctx context.Context, in *v1.GetConversationRequest) (*v1.Conversation, error) {
	c, err := s.config.Chats.Get(ctx, auth(ctx).Claims.Subject, in.ConversationId)
	if err != nil {
		return nil, err
	}
	return wireConversation(c), nil
}

// UpdateConversation implements the authenticated UpdateConversation mobile RPC.
func (s *Server) UpdateConversation(ctx context.Context, in *v1.UpdateConversationRequest) (*v1.Conversation, error) {
	out, err := s.mutate(ctx, "UpdateConversation", in.Operation, in, new(v1.Conversation), func(ctx context.Context) (proto.Message, error) {
		var set *conversations.Settings
		if in.Settings != nil {
			parsed, err := settings(in.Settings)
			if err != nil {
				return nil, err
			}
			if _, err = s.config.Engine.Resolve(ctx, auth(ctx).Claims.Subject, parsed.ProfileID); err != nil {
				return nil, conversations.ErrInvalid
			}
			set = &parsed
		}
		c, err := s.config.Chats.Update(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.ExpectedVersion, in.Title, set)
		return wireConversation(c), err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Conversation](out)
}

// ArchiveConversation implements the authenticated ArchiveConversation mobile RPC.
func (s *Server) ArchiveConversation(ctx context.Context, in *v1.ArchiveConversationRequest) (*v1.Conversation, error) {
	out, err := s.mutate(ctx, "ArchiveConversation", in.Operation, in, new(v1.Conversation), func(ctx context.Context) (proto.Message, error) {
		c, err := s.config.Chats.Archive(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.ExpectedVersion, in.Archived)
		return wireConversation(c), err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Conversation](out)
}

// ClearHistory implements the authenticated ClearHistory mobile RPC.
func (s *Server) ClearHistory(ctx context.Context, in *v1.ClearHistoryRequest) (*v1.Conversation, error) {
	out, err := s.mutate(ctx, "ClearHistory", in.Operation, in, new(v1.Conversation), func(ctx context.Context) (proto.Message, error) {
		c, err := s.config.Chats.Clear(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.ExpectedHistoryGeneration)
		return wireConversation(c), err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Conversation](out)
}

// ListMessages implements the authenticated ListMessages mobile RPC.
func (s *Server) ListMessages(ctx context.Context, in *v1.ListMessagesRequest) (*v1.ListMessagesResponse, error) {
	messages, next, err := s.config.Chats.Messages(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.HistoryGeneration, pageSize(in.PageSize), in.PageToken)
	if err != nil {
		return nil, err
	}
	out := &v1.ListMessagesResponse{NextPageToken: next, HistoryGeneration: in.HistoryGeneration}
	for i := range messages {
		m := &messages[i]
		role := v1.MessageRole_MESSAGE_ROLE_USER
		if m.Role == "assistant" {
			role = v1.MessageRole_MESSAGE_ROLE_ASSISTANT
		}
		out.Messages = append(out.Messages, &v1.Message{MessageId: m.ID, ConversationId: m.ConversationID, TurnId: m.TurnID, TurnSeq: m.TurnSeq, MessageSeq: m.MessageSeq, HistoryGeneration: m.Generation, Role: role, Text: m.Text, CreatedAt: timestamppb.New(m.CreatedAt)})
	}
	return out, nil
}

// SendMessage implements the authenticated SendMessage mobile RPC.
func (s *Server) SendMessage(ctx context.Context, in *v1.SendMessageRequest) (*v1.Turn, error) {
	out, err := s.mutate(ctx, "SendMessage", in.Operation, in, new(v1.Turn), func(ctx context.Context) (proto.Message, error) {
		t, _, err := s.config.Engine.Accept(engine.WithAuthority(ctx, s.config.OAuth.Authority(auth(ctx).Claims)), auth(ctx).Claims.Subject, in.ConversationId, conversations.Input{ClientMessageID: in.ClientMessageId, Generation: in.ExpectedHistoryGeneration, Text: in.Text})
		return wireTurn(t), err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Turn](out)
}

// GetTurn implements the authenticated GetTurn mobile RPC.
func (s *Server) GetTurn(ctx context.Context, in *v1.GetTurnRequest) (*v1.Turn, error) {
	var t conversations.Turn
	var err error
	switch key := in.Lookup.(type) {
	case *v1.GetTurnRequest_TurnId:
		t, err = s.config.Chats.GetTurn(ctx, auth(ctx).Claims.Subject, in.ConversationId, key.TurnId)
	case *v1.GetTurnRequest_ClientMessageId:
		t, err = s.config.Chats.GetTurnByClientID(ctx, auth(ctx).Claims.Subject, in.ConversationId, key.ClientMessageId)
	default:
		return nil, conversations.ErrInvalid
	}
	return wireTurn(t), err
}

// CancelTurn implements the authenticated CancelTurn mobile RPC.
func (s *Server) CancelTurn(ctx context.Context, in *v1.CancelTurnRequest) (*v1.Turn, error) {
	out, err := s.mutate(ctx, "CancelTurn", in.Operation, in, new(v1.Turn), func(ctx context.Context) (proto.Message, error) {
		t, err := s.config.Chats.Cancel(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.TurnId, in.HistoryGeneration)
		return wireTurn(t), err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Turn](out)
}

// RespondToPrompt implements the authenticated RespondToPrompt mobile RPC.
func (s *Server) RespondToPrompt(ctx context.Context, in *v1.RespondToPromptRequest) (*v1.Prompt, error) {
	if in.Binding == nil {
		return nil, conversations.ErrInvalid
	}
	out, err := s.mutate(ctx, "RespondToPrompt", in.Operation, in, new(v1.Prompt), func(ctx context.Context) (proto.Message, error) {
		p := auth(ctx).Claims
		b := in.Binding
		prompt, err := s.config.Chats.Decide(ctx, p.Subject, p.DeviceID, b.ConversationId, b.TurnId, b.PromptId, b.HistoryGeneration, in.ChoiceId, "", "", nil)
		return wirePrompt(prompt), err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Prompt](out)
}

// RespondToConfirmation implements the authenticated RespondToConfirmation mobile RPC.
func (s *Server) RespondToConfirmation(ctx context.Context, in *v1.RespondToConfirmationRequest) (*v1.Prompt, error) {
	if in.Binding == nil || in.GrantedMaxBytes != 0 {
		return nil, conversations.ErrInvalid
	}
	decision := ""
	switch in.Decision {
	case v1.Decision_DECISION_APPROVE:
		decision = "approve"
	case v1.Decision_DECISION_REJECT:
		decision = "deny"
	default:
		return nil, conversations.ErrInvalid
	}
	out, err := s.mutate(ctx, "RespondToConfirmation", in.Operation, in, new(v1.Prompt), func(ctx context.Context) (proto.Message, error) {
		p := auth(ctx).Claims
		b := in.Binding
		prompt, err := s.config.Chats.Decide(ctx, p.Subject, p.DeviceID, b.ConversationId, b.TurnId, b.PromptId, b.HistoryGeneration, decision, in.SecurityOperationId, in.Nonce, in.FingerprintSha256, conversations.ApprovalAuthority(s.config.OAuth.Authority(p)))
		return wirePrompt(prompt), err
	})
	if err != nil {
		return nil, err
	}
	return typedResponse[*v1.Prompt](out)
}
func subscriptions(sub *v1.Subscription) ([]string, error) {
	if sub != nil {
		switch f := sub.Filter.(type) {
		case *v1.Subscription_AllOwned:
			if f.AllOwned != nil {
				return nil, nil
			}
		case *v1.Subscription_Selected:
			if f.Selected != nil && len(f.Selected.ConversationIds) > 0 {
				return f.Selected.ConversationIds, nil
			}
		}
	}
	return nil, conversations.ErrInvalid
}

// GetSnapshot implements the authenticated GetSnapshot mobile RPC.
func (s *Server) GetSnapshot(ctx context.Context, in *v1.GetSnapshotRequest) (*v1.ChatSnapshot, error) {
	ids, err := subscriptions(in.Subscription)
	if err != nil {
		return nil, err
	}
	snap, err := s.config.Chats.Snapshot(ctx, auth(ctx).Claims.Subject, ids, pageSize(in.PageSize), in.PageToken)
	if err != nil {
		return nil, err
	}
	out := &v1.ChatSnapshot{SnapshotId: snap.ID, Cursor: snap.Cursor, Watermark: snap.Watermark, NextPageToken: snap.NextPage, ExpiresAt: timestamppb.New(snap.ExpiresAt)}
	for i := range snap.Items {
		item := &snap.Items[i]
		c := &v1.ConversationSnapshot{Conversation: wireConversation(item.Conversation)}
		for i := range item.ActiveTurns {
			c.ActiveTurns = append(c.ActiveTurns, wireTurn(item.ActiveTurns[i]))
		}
		for _, taskID := range item.TaskIDs {
			c.Tasks = append(c.Tasks, &v1.TaskReference{TaskId: taskID})
		}
		for i := range item.Prompts {
			c.PendingPrompts = append(c.PendingPrompts, wirePrompt(item.Prompts[i]))
		}
		out.Conversations = append(out.Conversations, c)
	}
	return out, nil
}

// MarkRead implements the authenticated MarkRead mobile RPC.
func (s *Server) MarkRead(ctx context.Context, in *v1.MarkReadRequest) (*v1.ReadPosition, error) {
	c, err := s.config.Chats.MarkRead(ctx, auth(ctx).Claims.Subject, in.ConversationId, in.HistoryGeneration, in.VisibleMessageSeq)
	if err != nil {
		return nil, err
	}
	return &v1.ReadPosition{ConversationId: c.ID, HistoryGeneration: c.Generation, MessageSeq: c.ReadSeq}, nil
}

// WatchEvents implements the authenticated WatchEvents mobile RPC.
func (s *Server) WatchEvents(in *v1.WatchEventsRequest, stream grpc.ServerStreamingServer[v1.ChatEvent]) error {
	if err := checkClient(in); err != nil {
		return err
	}
	ctx := stream.Context()
	owner := auth(ctx).Claims.Subject
	ids, err := subscriptions(in.Subscription)
	if err != nil {
		return err
	}
	cursor := in.Cursor
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	headersSent := false
	for {
		events, next, err := s.config.Chats.Events(ctx, owner, ids, in.SnapshotId, cursor, 100)
		if err != nil {
			return err
		}
		if !headersSent {
			if checkErr1 := stream.SendHeader(metadata.MD{}); checkErr1 != nil {
				return checkErr1
			}
			headersSent = true
		}
		for _, e := range events {
			out := &v1.ChatEvent{EventId: e.ID, Cursor: e.Cursor, DurableSeq: e.Seq, ConversationId: e.ConversationID, TurnId: e.TurnID, HistoryGeneration: e.Generation}
			c, err := s.config.Chats.Get(ctx, owner, e.ConversationID)
			if err != nil {
				return err
			}
			switch {
			case e.Type == "task.changed":
				out.Body = &v1.ChatEvent_Task{Task: &v1.TaskReference{TaskId: e.InputMessageID}}
			case c.Generation != e.Generation:
				out.HistoryGeneration = c.Generation
				out.Body = &v1.ChatEvent_Reset_{Reset_: &v1.GenerationReset{NewGeneration: c.Generation}}
			case e.Type == "prompt.changed":
				p, stepErr2 := s.config.Chats.Prompt(ctx, owner, e.ConversationID, e.TurnID, e.InputMessageID)
				if stepErr2 != nil {
					return stepErr2
				}
				out.Body = &v1.ChatEvent_Prompt{Prompt: wirePrompt(p)}
			case e.TurnID != "":
				t, stepErr3 := s.config.Chats.GetTurn(ctx, owner, e.ConversationID, e.TurnID)
				if stepErr3 != nil {
					return stepErr3
				}
				out.Body = &v1.ChatEvent_Turn{Turn: wireTurn(t)}
			default:
				out.Body = &v1.ChatEvent_Conversation{Conversation: wireConversation(c)}
			}
			if checkErr4 := stream.Send(out); checkErr4 != nil {
				return checkErr4
			}
		}
		cursor = next
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// The mutation cache is type-erased; verify its output before returning an RPC.
func typedResponse[T proto.Message](message proto.Message) (T, error) {
	result, ok := message.(T)
	if !ok {
		var empty T
		return empty, conversations.ErrInvalid
	}
	return result, nil
}
