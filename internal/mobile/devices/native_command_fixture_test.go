package devices

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Synthetic cross-language wire samples from the same marshal used by tasks.go.
func TestWriteNativeCommandFixture(t *testing.T) {
	path := os.Getenv("IULITA_NATIVE_COMMAND_FIXTURE")
	if path == "" {
		t.Skip("explicit synthetic fixture output required")
	}
	now := time.UnixMilli(1792135000123)
	rows := make([]map[string]any, 0, 3)
	for _, kind := range []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS, v1.ActionKind_ACTION_KIND_READ_LOCATION, v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS} {
		c := &v1.Command{ProtocolVersion: 1, CommandId: uuid.NewString(), TaskId: uuid.NewString(), DeviceId: uuid.NewString(), ServerSeq: 42, IssuedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(time.Minute)), SessionId: uuid.NewString(), SessionGeneration: 1, CoordinatorEpoch: 3, TaskStep: 1, Origin: &v1.TaskOrigin{ChannelType: "mobile", ChannelInstance: "mobile", BindingRevision: PolicyRevision, ConversationId: uuid.NewString(), TurnId: uuid.NewString()}, Envelope: &v1.TaskEnvelope{Goal: kind.String(), AllowedActions: []v1.ActionKind{kind}, ExpiresAt: timestamppb.New(now.Add(time.Minute))}, Action: &v1.Command_Read{Read: &v1.ReadAction{Kind: kind}}, ClockId: uuid.NewString()}
		unsigned, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(unsigned)
		c.PayloadDigestSha256 = sum[:]
		wire, err := proto.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, map[string]any{"wire": wire, "unsigned_wire": unsigned})
	}
	b, err := json.Marshal(map[string]any{"now_ms": now.UnixMilli(), "commands": rows})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
