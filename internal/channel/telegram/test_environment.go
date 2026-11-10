package telegram

// IsTestEnvironment reports the immutable transport chosen at construction.
// It is not a configuration-only UI gate and cannot be toggled on a live bot.
func (c *Channel) IsTestEnvironment() bool { return c.testEnvironment }
