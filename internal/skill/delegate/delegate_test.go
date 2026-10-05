package delegate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
)

type capturingRouter struct{ request llm.Request }

func (p *capturingRouter) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	p.request = r
	return llm.Response{Content: "done"}, nil
}
func TestDelegateBackgroundDefaultPreservesExplicitHints(t *testing.T) {
	for _, tc := range []struct{ input, role, hint, profile string }{
		{`{"prompt":"task"}`, "background", "", ""},
		{`{"prompt":"task","provider":"complex"}`, "", "complex", ""},
		{`{"prompt":"task","provider":"custom_route"}`, "", "custom_route", ""},
		{`{"prompt":"task","provider":"complex","profile_id":"explicit"}`, "background", "complex", "explicit"},
	} {
		p := &capturingRouter{}
		s := NewRouted(p)
		if _, err := s.Execute(context.Background(), json.RawMessage(tc.input)); err != nil {
			t.Fatal(err)
		}
		if p.request.Role != tc.role || p.request.RouteHint != tc.hint || p.request.ProfileID != tc.profile || p.request.Operation != "delegate" {
			t.Fatalf("wrong route: %+v", p.request)
		}
	}
}
