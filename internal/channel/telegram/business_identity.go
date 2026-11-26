package telegram

import business "github.com/iulita-ai/iulita/internal/telegrambusiness"

// SecretaryPollingEnabled excludes the isolated API test namespace.
func (c *Channel) SecretaryPollingEnabled() bool {
	return c.business != nil && c.businessPoll != nil && !c.testEnvironment
}

// BusinessDeliveryGeneration returns the read-locked local delivery identity;
// it does not authorize an account operation or perform a Telegram request.
func (c *Channel) BusinessDeliveryGeneration() string {
	return c.businessDeliveryGeneration()
}

// SetBusinessRuntimeGuard installs the local runtime authority before channel
// publication. It is startup-only; a missing guard denies account dispatch.
func (c *Channel) SetBusinessRuntimeGuard(current func(business.Scope) string) {
	c.businessRuntime = current
}
