package telegram

// noteFailure records a best-effort transport or bookkeeping failure without
// logging provider error text, which may contain credentials or private data.
// Callers still decide whether to stop or retain durable work for retry.
func (c *Channel) noteFailure(operation string, err error) {
	if err != nil && c.logger != nil {
		c.logger.Warn(operation)
	}
}
