package mobile

import (
	"fmt"
	"strings"

	mobilev1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// ProtocolVersion identifies the supported mobile wire contract.
const ProtocolVersion uint32 = 1

// CheckProtocolVersion rejects unsupported wire protocol versions.
func CheckProtocolVersion(version uint32) error {
	if version != ProtocolVersion {
		return fmt.Errorf("unsupported mobile protocol version")
	}
	return nil
}

var mobileScopes = map[string]bool{
	"chats:read": true, "chats:write": true, "devices:read": true,
	"devices:receive": true, "devices:sync": true, "devices:manage": true,
}

// MethodPolicy returns a copy of the schema policy. Unknown/unannotated methods
// fail closed; this is static contract metadata, NOT token authentication.
func MethodPolicy(fullMethod string) (*mobilev1.RpcPolicy, error) {
	service, method, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok || !strings.HasPrefix(service, "iulita.mobile.v1.") || strings.Contains(method, "/") {
		return nil, fmt.Errorf("unknown mobile method")
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return nil, fmt.Errorf("unknown mobile service")
	}
	s, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("invalid mobile service")
	}
	m := s.Methods().ByName(protoreflect.Name(method))
	if m == nil || !proto.HasExtension(m.Options(), mobilev1.E_RpcPolicy) {
		return nil, fmt.Errorf("missing mobile method policy")
	}
	p, ok := proto.GetExtension(m.Options(), mobilev1.E_RpcPolicy).(*mobilev1.RpcPolicy)
	if !ok || p == nil || p.Feature == mobilev1.Feature_FEATURE_UNSPECIFIED ||
		p.Stage == mobilev1.TokenStage_TOKEN_STAGE_UNSPECIFIED || p.Binding == mobilev1.ResourceBinding_RESOURCE_BINDING_UNSPECIFIED ||
		p.Feature.Descriptor().Values().ByNumber(p.Feature.Number()) == nil ||
		p.Stage.Descriptor().Values().ByNumber(p.Stage.Number()) == nil ||
		p.Binding.Descriptor().Values().ByNumber(p.Binding.Number()) == nil {
		return nil, fmt.Errorf("invalid mobile method policy")
	}
	for _, scope := range p.Scopes {
		if !mobileScopes[scope] {
			return nil, fmt.Errorf("unknown mobile scope")
		}
	}
	cloned := new(mobilev1.RpcPolicy)
	proto.Merge(cloned, p)
	return cloned, nil
}

// ClientFrameScopes Control scopes are checked again for EVERY frame in addition to both scopes
// at stream opening. Server-issued epoch/device/lease checks are BE24/BE25.
func ClientFrameScopes(frame *mobilev1.ClientControlFrame) ([]string, error) {
	if frame == nil || CheckProtocolVersion(frame.GetProtocolVersion()) != nil {
		return nil, fmt.Errorf("invalid control frame version")
	}
	switch body := frame.Body.(type) {
	case *mobilev1.ClientControlFrame_Hello:
		if body != nil && body.Hello != nil {
			return []string{"devices:receive", "devices:sync"}, nil
		}
	case *mobilev1.ClientControlFrame_Capabilities:
		if body != nil && body.Capabilities != nil {
			return []string{"devices:sync"}, nil
		}
	case *mobilev1.ClientControlFrame_Session:
		if body != nil && body.Session != nil {
			return []string{"devices:sync"}, nil
		}
	case *mobilev1.ClientControlFrame_Receipt:
		if body != nil && body.Receipt != nil {
			return []string{"devices:sync"}, nil
		}
	case *mobilev1.ClientControlFrame_Renew:
		if body != nil && body.Renew != nil {
			return []string{"devices:sync"}, nil
		}
	}
	return nil, fmt.Errorf("unknown or empty client control frame")
}

// ServerFrameScopes returns the required scopes for each server control frame.
func ServerFrameScopes(frame *mobilev1.ServerControlFrame) ([]string, error) {
	if frame == nil || CheckProtocolVersion(frame.GetProtocolVersion()) != nil {
		return nil, fmt.Errorf("invalid control frame version")
	}
	switch body := frame.Body.(type) {
	case *mobilev1.ServerControlFrame_Welcome:
		if body != nil && body.Welcome != nil {
			return []string{"devices:receive", "devices:sync"}, nil
		}
	case *mobilev1.ServerControlFrame_ReceiptAck:
		if body != nil && body.ReceiptAck != nil {
			return []string{"devices:sync"}, nil
		}
	case *mobilev1.ServerControlFrame_Command:
		if body != nil && body.Command != nil {
			return []string{"devices:receive"}, nil
		}
	case *mobilev1.ServerControlFrame_Lease:
		if body != nil && body.Lease != nil {
			return []string{"devices:receive"}, nil
		}
	case *mobilev1.ServerControlFrame_Revoked:
		if body != nil && body.Revoked != nil {
			return []string{"devices:receive"}, nil
		}
	case *mobilev1.ServerControlFrame_Task:
		if body != nil && body.Task != nil {
			return []string{"devices:receive"}, nil
		}
	}
	return nil, fmt.Errorf("unknown or empty server control frame")
}

// ValidateCommandShape checks only the wire discriminator. It NEVER issues a
// grant: device identity, task envelope, snapshot, TTL, lease, local approval
// and journal checks are still mandatory in the future executor.
func ValidateCommandShape(command *mobilev1.Command) error {
	if command == nil || CheckProtocolVersion(command.GetProtocolVersion()) != nil {
		return fmt.Errorf("invalid command version")
	}
	switch action := command.Action.(type) {
	case *mobilev1.Command_Read:
		if action != nil && action.Read != nil {
			switch action.Read.Kind {
			case mobilev1.ActionKind_ACTION_KIND_READ_STATUS, mobilev1.ActionKind_ACTION_KIND_READ_LOCATION, mobilev1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS:
				return nil
			}
		}
	case *mobilev1.Command_System:
		if action != nil && action.System != nil {
			switch body := action.System.Body.(type) {
			case *mobilev1.SystemAction_TorchEnabled:
				if body != nil {
					return nil
				}
			case *mobilev1.SystemAction_Navigation:
				if body != nil && body.Navigation != nil {
					return nil
				}
			}
		}
	case *mobilev1.Command_Ui:
		if action != nil && action.Ui != nil {
			switch action.Ui.Kind {
			case mobilev1.ActionKind_ACTION_KIND_UI_OBSERVE, mobilev1.ActionKind_ACTION_KIND_UI_CLICK, mobilev1.ActionKind_ACTION_KIND_UI_SET_TEXT, mobilev1.ActionKind_ACTION_KIND_UI_SCROLL, mobilev1.ActionKind_ACTION_KIND_UI_SUBMIT:
				return nil
			}
		}
	}
	return fmt.Errorf("unknown or mismatched command action")
}
