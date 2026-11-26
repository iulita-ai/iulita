package channelmgr

import (
	"context"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

// SetTelegramBusiness installs the account service before StartAll. Ordinary
// private assistant routing remains a distinct trust domain.
func (m *Manager) SetTelegramBusiness(service *business.Service, poll business.PollRepository) {
	m.business = service
	m.businessPoll = poll
}

// GetConnection refreshes only the exact currently running bot connection.
func (m *Manager) GetConnection(ctx context.Context, key business.Key) (business.RemoteConnection, error) {
	m.mu.RLock()
	mc := m.running[key.InstanceID]
	m.mu.RUnlock()
	if mc == nil || mc.tg == nil || mc.tg.BusinessBotID() != key.BotID {
		return business.RemoteConnection{}, business.ErrUnavailable
	}
	return mc.tg.BusinessConnection(ctx, key)
}

// Execute admits a reviewed operation through its exact running bot transport.
func (m *Manager) Execute(ctx context.Context, request business.Request) (business.Receipt, error) {
	m.mu.RLock()
	mc := m.running[request.Action.Scope.Key.InstanceID]
	m.mu.RUnlock()
	if mc == nil || mc.tg == nil || mc.tg.BusinessBotID() != request.Action.Scope.Key.BotID {
		return business.Receipt{}, business.ErrUnavailable
	}
	return mc.tg.ExecuteBusiness(ctx, request)
}

// Descriptor identifies the scoped running attachment processors for consent.
func (m *Manager) Descriptor(scope business.Scope) string {
	m.mu.RLock()
	mc := m.running[scope.Key.InstanceID]
	m.mu.RUnlock()
	if mc == nil || mc.tg == nil || mc.tg.BusinessBotID() != scope.Key.BotID {
		return ""
	}
	return mc.tg.BusinessSourceDescriptor()
}

// BusinessGeneration identifies the exact live transport. Stopped, cancelled or
// replaced instances cannot authorize a prepared model request from a dashboard.
func (m *Manager) BusinessGeneration(scope business.Scope) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mc := m.running[scope.Key.InstanceID]
	if mc == nil || mc.runtimeContext == nil || mc.runtimeContext.Err() != nil || mc.tg == nil || mc.tg.BusinessBotID() != scope.Key.BotID || !mc.tg.SecretaryPollingEnabled() {
		return ""
	}
	return mc.tg.BusinessDeliveryGeneration()
}

// Process extracts bounded attachments without ordinary assistant access.
func (m *Manager) Process(ctx context.Context, scope business.Scope, messages []business.Message) (business.ProcessedSource, error) {
	m.mu.RLock()
	mc := m.running[scope.Key.InstanceID]
	m.mu.RUnlock()
	if mc == nil || mc.tg == nil || mc.tg.BusinessBotID() != scope.Key.BotID {
		return business.ProcessedSource{}, business.ErrUnavailable
	}
	return mc.tg.ProcessBusinessSources(ctx, scope, messages)
}
