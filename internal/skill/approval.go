package skill

// ApprovalLevel controls when a skill requires human confirmation before execution.
type ApprovalLevel int

const (
	// ApprovalAuto executes immediately without confirmation. Default for read-only skills.
	ApprovalAuto ApprovalLevel = iota
	// ApprovalPrompt requires in-chat user confirmation (yes/no reply).
	// Used for write operations: calendar events, task creation, reminders.
	ApprovalPrompt
	// ApprovalManual requires admin-role user confirmation.
	// Used for destructive or privileged operations: shell_exec, bulk delete.
	ApprovalManual
)

// ApprovalDeclarer is an optional interface skills implement to declare their approval level.
// Skills that don't implement this default to ApprovalAuto.
type ApprovalDeclarer interface {
	ApprovalLevel() ApprovalLevel
}
