package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

func TestScheduledJobRevalidatesPersistedAuthorityBeforeModel(t *testing.T) {
	for _, test := range []string{"missing", "disabled", "foreign-owner", "changed-prompt", "changed-route"} {
		t.Run(test, func(t *testing.T) {
			ctx := context.Background()
			s := memStore(t)
			provider := &fixedProvider{content: "must not run"}
			sender := &captureSender{}
			j := &domain.AgentJob{ID: 10, UserID: "alice", DeliveryChatID: "tgconv:one", Prompt: "approved", Enabled: true}
			if test != "missing" {
				if test == "disabled" {
					j.Enabled = false
				}
				if e := s.CreateAgentJob(ctx, j); e != nil {
					t.Fatal(e)
				}
				if test == "disabled" {
					j.Enabled = false
					if e := s.UpdateAgentJob(ctx, j); e != nil {
						t.Fatal(e)
					}
				}
			}
			p := agentJobPayload{JobID: 10, UserID: "alice", DeliveryChatID: "tgconv:one", Prompt: "approved"}
			switch test {
			case "foreign-owner":
				p.UserID = "mallory"
			case "changed-prompt":
				p.Prompt = "unapproved"
			case "changed-route":
				p.DeliveryChatID = "tgconv:other"
			}
			raw, _ := json.Marshal(p)
			h := NewAgentJobHandler(s, provider, nil, nil, sender, zap.NewNop())
			if _, e := h.Handle(ctx, string(raw)); e == nil {
				t.Fatal("stale/foreign payload accepted")
			}
			if provider.calls != 0 || len(sender.msgs) != 0 {
				t.Fatal("unauthorized job reached model/delivery")
			}
		})
	}
}
