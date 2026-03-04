package console

import (
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/i18n"
)

// slashCommand represents a locally-handled console command.
type slashCommand struct {
	name string
	desc string
	fn   func(m *tuiModel) string // returns text to display (empty = no output)
}

func builtinCommands(m *tuiModel) []slashCommand {
	t := func(key string) string { return i18n.T(m.ctx, key) }
	return []slashCommand{
		{"/help", t("CommandHelpDesc"), cmdHelp},
		{"/status", t("CommandStatusDesc"), cmdStatus},
		{"/compact", t("CommandCompactDesc"), nil}, // handled async in Update
		{"/clear", t("CommandClearDesc"), cmdClear},
		{"/quit", t("CommandQuitDesc"), nil}, // handled specially in Update
		{"/exit", t("CommandExitDesc"), nil}, // alias
	}
}

func cmdHelp(m *tuiModel) string {
	var b strings.Builder
	b.WriteString(i18n.T(m.ctx, "CommandHelpHeader"))
	b.WriteString("\n\n")
	for _, cmd := range builtinCommands(m) {
		b.WriteString("  ")
		b.WriteString(m.styles.helpKey.Render(cmd.name))
		b.WriteString("  ")
		b.WriteString(m.styles.helpDesc.Render(cmd.desc))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.styles.helpDesc.Render(i18n.T(m.ctx, "CommandHelpFooter")))
	return b.String()
}

func cmdStatus(m *tuiModel) string {
	var b strings.Builder
	b.WriteString(m.styles.helpKey.Render(i18n.T(m.ctx, "CommandStatusHeader")))
	b.WriteString("\n\n")

	if sp := m.statusProvider; sp != nil {
		enabled := sp.EnabledSkills()
		total := sp.TotalSkills()
		disabled := total - enabled
		b.WriteString("  " + i18n.T(m.ctx, "CommandStatusSkills", map[string]any{"Enabled": enabled, "Disabled": disabled}) + "\n")

		cost := sp.DailyCost()
		b.WriteString("  " + i18n.T(m.ctx, "CommandStatusCost", map[string]any{"Cost": fmt.Sprintf("%.4f", cost)}) + "\n")

		inTok, outTok, reqs := sp.SessionStats()
		b.WriteString("  " + i18n.T(m.ctx, "CommandStatusSession", map[string]any{"Requests": reqs, "In": inTok, "Out": outTok}) + "\n")
	} else {
		b.WriteString("  " + i18n.T(m.ctx, "CommandStatusUnavailable") + "\n")
	}

	b.WriteString("  " + i18n.T(m.ctx, "CommandStatusMessages", map[string]any{"Count": m.msgCount}) + "\n")

	return b.String()
}

func cmdClear(m *tuiModel) string {
	m.messages = m.messages[:0]
	m.streamBuf = ""
	m.refreshViewport()
	return ""
}

// trySlashCommand checks if input is a locally-handled command.
// Returns (handled bool, output string).
func trySlashCommand(m *tuiModel, input string) (bool, string) {
	trimmed := strings.TrimSpace(input)
	for _, cmd := range builtinCommands(m) {
		if strings.EqualFold(trimmed, cmd.name) {
			if cmd.fn == nil {
				return true, "" // /quit, /exit, /compact handled in Update
			}
			return true, cmd.fn(m)
		}
	}
	return false, ""
}
