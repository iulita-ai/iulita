package config

import (
	"fmt"
	"strconv"
)

// ApplyTelegramDayOverrides applies effective persisted settings before channel
// composition. Missing keys preserve compiled defaults; false is an explicit opt-out.
func ApplyTelegramDayOverrides(cfg *TelegramConfig, get func(string) (string, bool)) error {
	next := *cfg
	for key, dst := range map[string]*bool{"telegram.day_widgets": &next.DayWidgets, "telegram.day_actions": &next.DayActions} {
		if raw, ok := get(key); ok {
			value, err := strconv.ParseBool(raw)
			if err != nil {
				return fmt.Errorf("%s must be a boolean", key)
			}
			*dst = value
		}
	}
	*cfg = next
	return nil
}
