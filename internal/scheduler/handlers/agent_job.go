package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

const TaskTypeAgentJob = "agent.job"

type agentJobPayload struct {
	JobID          int64  `json:"job_id"`
	JobName        string `json:"job_name"`
	Prompt         string `json:"prompt"`
	DeliveryChatID string `json:"delivery_chat_id"`
}

// AgentJobHandler executes user-defined LLM prompts as scheduled tasks.
type AgentJobHandler struct {
	store    storage.Repository
	provider llm.Provider
	sender   channel.MessageSender
	logger   *zap.Logger
}

func NewAgentJobHandler(store storage.Repository, provider llm.Provider, sender channel.MessageSender, logger *zap.Logger) *AgentJobHandler {
	return &AgentJobHandler{store: store, provider: provider, sender: sender, logger: logger}
}

func (h *AgentJobHandler) Type() string { return TaskTypeAgentJob }

func (h *AgentJobHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p agentJobPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid payload: %w", err)
	}

	h.logger.Info("executing agent job", zap.Int64("job_id", p.JobID), zap.String("name", p.JobName))

	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: "You are a helpful assistant executing a scheduled task. Be concise and actionable.",
		Message:      p.Prompt,
	})
	if err != nil {
		return "", fmt.Errorf("agent job LLM call: %w", err)
	}

	content := strings.TrimSpace(resp.Content)
	if content == "" {
		return `{"status":"empty_response"}`, nil
	}

	// Deliver result to chat if configured.
	if p.DeliveryChatID != "" && h.sender != nil {
		msg := fmt.Sprintf("**Agent Job: %s**\n\n%s", p.JobName, content)
		if err := h.sender.SendMessage(ctx, p.DeliveryChatID, msg); err != nil {
			h.logger.Error("failed to deliver agent job result",
				zap.Int64("job_id", p.JobID), zap.Error(err))
		}
	}

	result, _ := json.Marshal(map[string]string{
		"status":  "completed",
		"preview": truncate(content, 200),
	})
	return string(result), nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
