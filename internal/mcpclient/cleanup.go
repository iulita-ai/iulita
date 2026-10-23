package mcpclient

import (
	"io"
	"log/slog"
)

// Read-only stream teardown must not expose provider error text to logs.
func closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil {
		slog.Debug("MCP stream cleanup incomplete")
	}
}
