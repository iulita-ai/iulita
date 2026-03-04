package notify

import "context"

// Notifier sends push notifications.
type Notifier interface {
	Send(ctx context.Context, title, message string) error
}
