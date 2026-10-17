package config

import "fmt"

func mobileEnvironmentKey(name string) (string, bool) {
	switch name {
	case "IULITA_SKILLS_CRAFT_NATIVE_OWNER_ID":
		return "skills.craft.native_owner_id", true
	case "IULITA_SKILLS_TODOIST_NATIVE_OWNER_ID":
		return "skills.todoist.native_owner_id", true
	case "IULITA_SKILLS_SLACK_OAUTH_NATIVE_OWNER_ID":
		return "skills.slack_oauth.native_owner_id", true
	case "IULITA_MOBILE_CHAT_ENABLED":
		return "mobile.chat_enabled", true
	case "IULITA_MOBILE_DEVICE_ENABLED":
		return "mobile.device_enabled", true
	case "IULITA_MOBILE_UI_ENABLED":
		return "mobile.ui_enabled", true
	case "IULITA_MOBILE_VISION_ENABLED":
		return "mobile.vision_enabled", true
	case "IULITA_MOBILE_RUNTIME_FILE":
		return "mobile.runtime_file", true
	default:
		return "", false
	}
}

// MobileConfig requests server features, not Android permissions or session grants.
// The zero value disables every feature. Enabling a flag also requires the
// corresponding implementation and mobile OAuth readiness (internal/mobile).
type MobileConfig struct {
	ChatEnabled   bool   `koanf:"chat_enabled"`
	DeviceEnabled bool   `koanf:"device_enabled"`
	UIEnabled     bool   `koanf:"ui_enabled"`
	VisionEnabled bool   `koanf:"vision_enabled"`
	RuntimeFile   string `koanf:"runtime_file"`
}

// Validate checks configuration dependencies and required values.
func (c MobileConfig) Validate() error {
	if c.UIEnabled && !c.DeviceEnabled {
		return fmt.Errorf("mobile.ui_enabled requires mobile.device_enabled")
	}
	if c.VisionEnabled && !c.UIEnabled {
		return fmt.Errorf("mobile.vision_enabled requires mobile.ui_enabled")
	}
	return nil
}
