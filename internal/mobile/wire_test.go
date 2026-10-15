package mobile

import (
	"context"
	"testing"

	mobilev1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestEveryRPCDeclaresPolicyAndCredentialStage(t *testing.T) {
	count := 0
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() != "iulita.mobile.v1" {
			return true
		}
		for i := 0; i < file.Services().Len(); i++ {
			service := file.Services().Get(i)
			for j := 0; j < service.Methods().Len(); j++ {
				method := service.Methods().Get(j)
				full := "/" + string(service.FullName()) + "/" + string(method.Name())
				policy, err := MethodPolicy(full)
				if err != nil {
					t.Errorf("%s: %v", full, err)
					continue
				}
				count++
				if method.Name() != "ActivateDevice" && method.Name() != "CompleteRecovery" && policy.Stage != mobilev1.TokenStage_TOKEN_STAGE_ACTIVE {
					t.Errorf("ordinary method accepts provisional credentials: %s", full)
				}
				if policy.Stage != mobilev1.TokenStage_TOKEN_STAGE_ACTIVE && (policy.Feature != mobilev1.Feature_FEATURE_IDENTITY || policy.Binding != mobilev1.ResourceBinding_RESOURCE_BINDING_TOKEN_DEVICE) {
					t.Errorf("provisional method not narrowed: %s", full)
				}
			}
		}
		return true
	})
	if count != 31 {
		t.Fatalf("review new or missing RPC policies: got %d, want 31", count)
	}
	for _, method := range []string{"", "/iulita.mobile.v2.ChatService/SendMessage", "/iulita.mobile.v1.ChatService/Login", "/ws/chat", "/iulita.mobile.v1.Turn/SendMessage"} {
		if _, err := MethodPolicy(method); err == nil {
			t.Fatalf("unknown method accepted: %s", method)
		}
	}
	policy, _ := MethodPolicy(mobilev1.DeviceService_Control_FullMethodName)
	if len(policy.Scopes) != 2 {
		t.Fatal("Control must require receive AND sync")
	}
	policy.Scopes[0] = "unsafe-global-scope"
	if _, err := MethodPolicy(mobilev1.DeviceService_Control_FullMethodName); err != nil {
		t.Fatal("caller mutated global method authority")
	}
}

func TestUnknownWireDiscriminatorsFailClosed(t *testing.T) {
	for _, version := range []uint32{0, 2, 999} {
		if err := CheckProtocolVersion(version); err == nil {
			t.Fatal("unknown version accepted")
		}
	}
	for _, kind := range []mobilev1.ActionKind{0, 999, mobilev1.ActionKind_ACTION_KIND_SET_TORCH} {
		command := &mobilev1.Command{ProtocolVersion: 1, Action: &mobilev1.Command_Read{Read: &mobilev1.ReadAction{Kind: kind}}}
		data, err := proto.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		decoded := new(mobilev1.Command)
		if err := proto.Unmarshal(data, decoded); err != nil {
			t.Fatal(err)
		}
		if ValidateCommandShape(decoded) == nil {
			t.Fatal("protobuf decoding permitted an unknown/mismatched action")
		}
	}
	// Future unknown oneof tags survive decoding but cannot authorize execution.
	unknown := &mobilev1.Command{ProtocolVersion: 1}
	unknown.ProtoReflect().SetUnknown([]byte{0xfa, 0x01, 0x00})
	data, _ := proto.Marshal(unknown)
	decoded := new(mobilev1.Command)
	if err := proto.Unmarshal(data, decoded); err != nil || ValidateCommandShape(decoded) == nil {
		t.Fatal("unknown command oneof accepted")
	}
	for _, frame := range []*mobilev1.ClientControlFrame{nil, {ProtocolVersion: 1}, {ProtocolVersion: 2}, {ProtocolVersion: 1, Body: (*mobilev1.ClientControlFrame_Hello)(nil)}} {
		if _, err := ClientFrameScopes(frame); err == nil {
			t.Fatal("empty/unknown control frame accepted")
		}
	}
	if _, err := ServerFrameScopes(&mobilev1.ServerControlFrame{ProtocolVersion: 1}); err == nil {
		t.Fatal("missing server body accepted")
	}
}

func TestMutationLookupAndAbsentMeasurementsRemainDistinct(t *testing.T) {
	request := &mobilev1.GetTurnRequest{ConversationId: "conversation", Lookup: &mobilev1.GetTurnRequest_ClientMessageId{ClientMessageId: "original-client-id"}}
	data, _ := proto.Marshal(request)
	decoded := new(mobilev1.GetTurnRequest)
	if err := proto.Unmarshal(data, decoded); err != nil || decoded.GetClientMessageId() != "original-client-id" || decoded.GetTurnId() != "" {
		t.Fatal("lost ACK lookup is not preserved")
	}
	zero := uint32(0)
	measurement := &mobilev1.DeviceStatus{BatteryPercent: &zero}
	data, _ = proto.Marshal(measurement)
	result := new(mobilev1.DeviceStatus)
	if err := proto.Unmarshal(data, result); err != nil || result.BatteryPercent == nil || result.Charging != nil {
		t.Fatal("inaccessible status collapsed into measured zero")
	}
}

func TestGeneratedDefaultsHaveNoImplementedAuthority(t *testing.T) {
	_, err := (&mobilev1.UnimplementedDeviceServiceServer{}).ActivateDevice(context.Background(), &mobilev1.ActivateDeviceRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatal("generated default unexpectedly activated a device")
	}
	_, err = (&mobilev1.UnimplementedChatServiceServer{}).SendMessage(context.Background(), &mobilev1.SendMessageRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatal("generated default unexpectedly accepted a turn")
	}
}
