package mobile

import (
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/config"
)

func TestBuildSupportsServerAAndKeepsUIAndVisionClosed(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		flags := config.MobileConfig{ChatEnabled: mask&1 != 0, DeviceEnabled: mask&2 != 0, UIEnabled: mask&4 != 0, VisionEnabled: mask&8 != 0}
		err := ValidateStartup(flags, BuildReadiness())
		if (err == nil) != (mask&12 == 0) {
			t.Fatalf("requested mask %d unexpectedly accepted: %v", mask, err)
		}
	}
}

func TestIndependentOAuthAndImplementationReadiness(t *testing.T) {
	flags := config.MobileConfig{DeviceEnabled: true, UIEnabled: true, VisionEnabled: true}
	for _, ready := range []Readiness{
		{Device: true, UI: true, Vision: true}, // Missing independent OAuth.
		{OAuth: true, UI: true, Vision: true},
		{OAuth: true, Device: true, Vision: true},
		{OAuth: true, Device: true, UI: true},
	} {
		if err := ValidateStartup(flags, ready); err == nil {
			t.Fatalf("incomplete implementation readiness accepted: %+v", ready)
		}
	}
	if err := ValidateStartup(flags, Readiness{OAuth: true, Device: true, UI: true, Vision: true}); err != nil {
		t.Fatal(err)
	}
	// A data-only installation need not expose mobile chat, or vice versa.
	if err := ValidateStartup(config.MobileConfig{ChatEnabled: true}, Readiness{OAuth: true, Chat: true}); err != nil {
		t.Fatal(err)
	}
}

func TestOverridesAreAtomicAndFailClosed(t *testing.T) {
	flags := config.MobileConfig{ChatEnabled: true}
	values := map[string]string{"mobile.chat_enabled": "false", "mobile.device_enabled": "bad-private-value"}
	get := func(key string) (string, bool) { v, ok := values[key]; return v, ok }
	err := ApplyOverrides(&flags, get)
	if err == nil || strings.Contains(err.Error(), "bad-private-value") || !flags.ChatEnabled {
		t.Fatal("malformed override changed state or disclosed its value")
	}
	values["mobile.device_enabled"] = "true"
	values["mobile.ui_enabled"] = "true"
	if err := ApplyOverrides(&flags, get); err != nil {
		t.Fatal(err)
	}
	if flags.ChatEnabled || !flags.DeviceEnabled || !flags.UIEnabled {
		t.Fatal("effective restart overrides were not applied")
	}
	if err := ValidateStartup(flags, BuildReadiness()); err == nil {
		t.Fatal("imported enabled flags bypassed build readiness")
	}
}
