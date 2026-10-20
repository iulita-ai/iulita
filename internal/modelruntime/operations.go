package modelruntime

import (
	"context"
	"strings"
)

// OperationAvailability reports the active Z.ai connection's nonsecret state.
// Readiness means credentials can be admitted, not that Agent API entitlement
// or upstream health has been checked. Other providers are not supported here.
func (m *Manager) OperationAvailability(provider string) string {
	if m == nil {
		return "runtime_unavailable"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.operationAvailabilityLocked(provider)
}

func (m *Manager) operationAvailabilityLocked(provider string) string {
	if m.closed || m.cipher == nil {
		return "runtime_unavailable"
	}
	if provider != "zai" {
		return "not_configured"
	}
	for _, forbidden := range m.state.Settings.Policy.ForbiddenProviders {
		if forbidden == provider {
			return "forbidden"
		}
	}
	c, ok := m.state.Connections[provider]
	if !ok {
		return "not_configured"
	}
	if _, denied := m.state.Denied[c.Connection.Generation]; denied || !c.Connection.Enabled {
		return "revoked"
	}
	if _, denied := m.state.DeniedCredentials[secretIdentity(provider, c.Secret, m.state.IdentitySalt)]; denied {
		return "revoked"
	}
	if strings.TrimSpace(c.Secret) == "" || c.Connection.Generation == "" {
		return "not_configured"
	}
	return "ready"
}

// WithOperationConnection admits one translation attempt against the current
// active Z.ai credential generation. The callback runs outside the manager lock
// with a cancellable context and a transient secret handle. Trusted Go callers
// must not retain, expose, log or persist the handle, or retry inside a callback.
// Every new attempt resolves the connection again and needs its own admission.
func (m *Manager) WithOperationConnection(ctx context.Context, provider, operation string, callback func(context.Context, Connection, uint64) error) error {
	if provider != "zai" || operation != "translation" {
		return failure("unsupported_operation", "This connection does not support the requested operation", 422)
	}
	if callback == nil {
		return failure("invalid_operation_callback", "An operation callback is required", 422)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil {
		return failure("runtime_unavailable", "The protected model runtime is unavailable", 503)
	}
	m.mu.Lock()
	availability := m.operationAvailabilityLocked(provider)
	if availability != "ready" {
		m.mu.Unlock()
		switch availability {
		case "runtime_unavailable":
			return failure("runtime_unavailable", "The protected model runtime is unavailable", 503)
		case "forbidden":
			return failure("forbidden_provider", "This model provider is forbidden", 403)
		case "revoked":
			return failure("credential_revoked", "The model credential is no longer active", 403)
		default:
			return failure("credential_required", "Configure a current model connection before using this operation", 422)
		}
	}
	connection := m.state.Connections[provider].handle()
	revision := m.state.Revision
	admitted, release, err := m.admitLocked(ctx, provider, connection.Generation, "")
	m.mu.Unlock()
	if err != nil {
		return err
	}
	defer release()
	if err := admitted.Err(); err != nil {
		return err
	}
	if err := callback(admitted, connection, revision); err != nil {
		return err
	}
	return admitted.Err()
}
