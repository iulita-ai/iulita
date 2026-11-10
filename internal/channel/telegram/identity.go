package telegram

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-telegram/bot"
)

var validBotUsername = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

// ConfigureAliases verifies candidates against Telegram's bot identity. Call only
// before Start; failed/removed aliases never become link or command targets.
func (c *Channel) ConfigureAliases(ctx context.Context, aliases []string, preferred string) error {
	if c.botID == 0 {
		return fmt.Errorf("bot identity unavailable")
	}
	// Username-based getChat is documented for groups/channels. Resolve our
	// numeric bot identity and accept only its reported active usernames.
	info, err := c.bot.GetChat(ctx, &bot.GetChatParams{ChatID: c.botID})
	if err != nil || info.ID != c.botID {
		return fmt.Errorf("bot aliases could not be verified")
	}
	known := append([]string{info.Username}, info.ActiveUsernames...)
	verified := make([]string, 0, len(known))
	seen := map[string]bool{}
	for _, name := range known {
		if validBotUsername.MatchString(name) && !seen[strings.ToLower(name)] {
			verified = append(verified, name)
			seen[strings.ToLower(name)] = true
		}
	}

	for _, a := range aliases {
		a = strings.TrimPrefix(a, "@")
		if !validBotUsername.MatchString(a) {
			return fmt.Errorf("invalid bot alias")
		}
		found := false
		for _, v := range known {
			if strings.EqualFold(a, v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("bot alias is not active on this identity")
		}
	}
	preferred = strings.TrimPrefix(preferred, "@")
	if preferred != "" && !strings.EqualFold(preferred, c.username) {
		found := false
		for _, a := range verified {
			if strings.EqualFold(a, preferred) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("preferred bot username is not verified")
		}
	}
	c.verifiedAliases = verified
	c.preferredUsername = preferred
	return nil
}
func (c *Channel) linkUsername() string {
	if c.preferredUsername != "" {
		return c.preferredUsername
	}
	return c.username
}
func (c *Channel) commandName(command string) (string, bool) {
	parts := strings.SplitN(command, "@", 2)
	if len(parts) == 1 {
		return command, true
	}
	if strings.EqualFold(parts[1], c.username) {
		return parts[0], true
	}
	for _, a := range c.verifiedAliases {
		if strings.EqualFold(parts[1], a) {
			return parts[0], true
		}
	}
	return "", false
}
