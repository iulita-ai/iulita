package llm

import "context"

// TaskProvider sends background consumers through the shared router, preserving
// explicit legacy hints while applying the current policy's background role.
type TaskProvider struct {
	inner           Provider
	hint, operation string
}

func NewTaskProvider(inner Provider, hint, operation string) *TaskProvider {
	return &TaskProvider{inner: inner, hint: hint, operation: operation}
}
func (p *TaskProvider) Complete(ctx context.Context, req Request) (Response, error) {
	if req.RouteHint == "" && req.ProfileID == "" {
		req.RouteHint = p.hint
	}
	if req.Role == "" && req.RouteHint == "" {
		req.Role = "background"
	}
	if req.Operation == "" {
		req.Operation = p.operation
	}
	return p.inner.Complete(ctx, req)
}
