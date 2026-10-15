// Package mobile defines the isolated mobile backend boundary and wire policy.
package mobile

import (
	"fmt"
	"strconv"

	"github.com/iulita-ai/iulita/internal/config"
)

// Readiness is implementation-owned, never supplied by config, token or client.
// OAuth is the independent mobile realm, not the legacy dashboard JWT service.
type Readiness struct {
	OAuth  bool
	Chat   bool
	Device bool
	UI     bool
	Vision bool
}

// BuildReadiness describes implemented server capabilities, not deployment or
// Android acceptance. All configuration flags remain off by default. UI and
// Vision require separate joint native gates and cannot be enabled in this build.
func BuildReadiness() Readiness { return Readiness{OAuth: true, Chat: true, Device: true} }

// ValidateStartup fails closed instead of silently accepting enabled flags
// for unavailable services. A ready server still cannot create a native grant.
func ValidateStartup(c config.MobileConfig, ready Readiness) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.ChatEnabled && !c.DeviceEnabled && !c.UIEnabled && !c.VisionEnabled {
		return nil
	}
	if !ready.OAuth {
		return fmt.Errorf("mobile features require the independent OAuth backend (BE12–BE19); it is not ready in this build")
	}
	for _, f := range []struct {
		name      string
		requested bool
		ready     bool
	}{
		{"chat", c.ChatEnabled, ready.Chat},
		{"device", c.DeviceEnabled, ready.Device},
		{"ui", c.UIEnabled, ready.UI && ready.Device},
		{"vision", c.VisionEnabled, ready.Vision && ready.UI && ready.Device},
	} {
		if f.requested && !f.ready {
			return fmt.Errorf("mobile.%s_enabled requires its implementation and acceptance gate", f.name)
		}
	}
	return nil
}

// ApplyOverrides resolves restart-only effective values after DB imports.
// Parse failures are errors; a malformed override must not become true or
// silently leave a different TOML value in effect. Values are never logged.
func ApplyOverrides(c *config.MobileConfig, get func(string) (string, bool)) error {
	next := *c
	for _, f := range []struct {
		key string
		dst *bool
	}{
		{"mobile.chat_enabled", &next.ChatEnabled},
		{"mobile.device_enabled", &next.DeviceEnabled},
		{"mobile.ui_enabled", &next.UIEnabled},
		{"mobile.vision_enabled", &next.VisionEnabled},
	} {
		if value, ok := get(f.key); ok {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean for %s", f.key)
			}
			*f.dst = parsed
		}
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*c = next
	return nil
}
