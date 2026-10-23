package skill

import (
	"context"
	"testing"
)

func TestMCPAndSSHOutputGuardsRemainIndependent(t *testing.T) {
	ctx := WithTurnTaint(context.Background())
	MarkExternalDataUsed(ctx)
	if !SlackSearchUsedInTurn(ctx) || RemoteOutputUsedInTurn(ctx) {
		t.Fatal("MCP external content lost its publication guard or acquired SSH authority")
	}
	MarkRemoteOutputUsed(ctx)
	if !RemoteOutputUsedInTurn(ctx) || !SlackSearchUsedInTurn(ctx) {
		t.Fatal("one source overwrote another source's guard")
	}
	if ConsumeRemoteOutputBudget(ctx, 64*1024) != 64*1024 || ConsumeRemoteOutputBudget(ctx, 1) != 0 {
		t.Fatal("SSH model output budget was weakened")
	}
	fresh := WithTurnTaint(ctx)
	if SlackSearchUsedInTurn(fresh) || RemoteOutputUsedInTurn(fresh) {
		t.Fatal("source state leaked into another turn")
	}
}
