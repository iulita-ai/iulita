package weather

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/defaults"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/security"
)

// CommandLocation records how a direct weather command selected its place.
// Source is a descriptive hint, never a claim of current GPS presence.
type CommandLocation struct {
	Location string
	Source   string
}

// LocationFactReader is optional: command location resolution needs chronology,
// rather than the relevance/popularity ordering of general memory recall.
type LocationFactReader interface {
	GetLocationFactsByUser(context.Context, string, int) ([]domain.Fact, error)
}

// ResolveCommandLocation reads only this owner's memory and this conversation's
// recent user turns. It never prompts, invokes a model or geolocates the server.
func (s *Skill) ResolveCommandLocation(ctx context.Context, owner, conversation string) (CommandLocation, error) {
	fallback := CommandLocation{Location: defaults.City, Source: "default"}
	if err := ctx.Err(); err != nil {
		return CommandLocation{}, err
	}
	if owner == "" || conversation == "" || s.store == nil {
		return CommandLocation{}, fmt.Errorf("weather location scope unavailable")
	}
	now := time.Now()
	history, err := s.store.GetHistory(ctx, conversation, 30)
	if err != nil {
		return CommandLocation{}, fmt.Errorf("reading recent weather location: %w", err)
	}
	if choice, recognized := recentCommandLocation(history, owner, conversation, now); recognized {
		if choice.Location == "" {
			return fallback, ctx.Err()
		}
		return choice, ctx.Err()
	}
	reader, ok := s.store.(LocationFactReader)
	if !ok {
		return CommandLocation{}, fmt.Errorf("weather location memory unavailable")
	}
	user, err := s.store.GetUser(ctx, owner)
	if err != nil || user == nil || user.ID != owner {
		return CommandLocation{}, fmt.Errorf("weather location owner unavailable")
	}
	facts, err := reader.GetLocationFactsByUser(ctx, owner, 200)
	if err != nil {
		return CommandLocation{}, err
	}
	// Defend the chronological contract at the boundary, including equal timestamps.
	sort.SliceStable(facts, func(i, j int) bool {
		if facts[i].CreatedAt.Equal(facts[j].CreatedAt) {
			return facts[i].ID > facts[j].ID
		}
		return facts[i].CreatedAt.After(facts[j].CreatedAt)
	})
	for i := range facts {
		fact := &facts[i]
		if fact.UserID != owner || fact.SourceType != "user" || fact.CreatedAt.IsZero() || fact.CreatedAt.After(now) {
			continue
		}
		location, temporary, recognized := commandLocationAssertion(fact.Content, user)
		if !recognized || (temporary && now.Sub(fact.CreatedAt) > 24*time.Hour) {
			continue
		}
		// A recognized retraction, contradiction or unsafe tail is a barrier:
		// never resurrect an older home assertion behind it.
		if location == "" {
			return fallback, ctx.Err()
		}
		return CommandLocation{Location: location, Source: "memory"}, ctx.Err()
	}
	return fallback, ctx.Err()
}

var commandPinLine = regexp.MustCompile(`^\[Location\]: (-?\d{1,3}\.\d+, -?\d{1,3}\.\d+)(?: \(±\d+(?:\.\d+)? (?:m|м|מ׳|米)\))?(?: \((?:live location — updates not tracked|живая геопозиция — обновления не отслеживаются|position en direct — mises à jour non suivies|ubicación en vivo — no se rastrean actualizaciones|מיקום חי — העדכונים אינם נעקבים|实时位置——不追踪更新)\))?$`)
var commandPlaceLine = regexp.MustCompile(`^\[Place\]: [^\r\n]+ \((-?\d{1,3}\.\d+, -?\d{1,3}\.\d+)\)$`)

// recentCommandLocation uses one newest-first sequence for pins, direct user
// assertions and retraction barriers. History is at most 30 stored rows; only
// five owner user turns within 24 hours qualify. Assistant summaries never do.
func recentCommandLocation(history []domain.ChatMessage, owner, conversation string, now time.Time) (CommandLocation, bool) {
	seen := 0
	for i := len(history) - 1; i >= 0 && seen < 5; i-- {
		m := history[i]
		if m.Role != domain.RoleUser || m.UserID != owner || m.ChatID != conversation {
			continue
		}
		seen++
		if m.CreatedAt.IsZero() || m.CreatedAt.After(now) || now.Sub(m.CreatedAt) > 24*time.Hour {
			continue
		}
		// Whole-message markers are descriptive user text, not native GPS proof.
		line := strings.ReplaceAll(strings.TrimSpace(m.Content), "\u200e", "")
		matches := commandPinLine.FindStringSubmatch(line)
		if matches == nil {
			matches = commandPlaceLine.FindStringSubmatch(line)
		}
		if len(matches) == 2 {
			if _, _, ok := parseCoordinatePair(matches[1]); ok {
				return CommandLocation{Location: matches[1], Source: "shared"}, true
			}
		}
		if strings.HasPrefix(line, "[Location]:") || strings.HasPrefix(line, "[Place]:") {
			return CommandLocation{}, true // invalid newer location cancels an old pin
		}
		// Third-person fact grammar is deliberately excluded in raw user text:
		// "User lives in ..." may be a forwarded example about another person.
		direct := false
		for _, prefix := range []string{"I ", "Я ", "My city: ", "My home city: ", "Мой город: ", "Мой город проживания: "} {
			if len(line) >= len(prefix) && strings.EqualFold(line[:len(prefix)], prefix) {
				direct = true
				break
			}
		}
		if !direct {
			continue
		}
		location, _, recognized := commandLocationAssertion(line, nil)
		if recognized {
			return CommandLocation{Location: location, Source: "context"}, true
		}
	}
	return CommandLocation{}, false
}

func commandLocationAssertion(content string, user *domain.User) (location string, temporary, recognized bool) {
	text := strings.TrimSpace(content)
	subjects := []string{"I", "User", "The user", "Я", "Пользователь", "Пользовательница"}
	if user != nil {
		for _, name := range []string{user.DisplayName, user.Username} {
			if name = strings.TrimSpace(name); name != "" {
				subjects = append(subjects, name)
			}
		}
	}
	for _, subject := range subjects {
		prefix := subject + " "
		if len(text) <= len(prefix) || !strings.EqualFold(text[:len(prefix)], prefix) {
			continue
		}
		tail := text[len(prefix):]
		for _, negative := range []string{
			"no longer live", "do not live", "does not live", "don't live", "doesn't live", "used to live",
			"no longer reside", "do not reside", "does not reside", "don't reside", "doesn't reside",
			"am no longer based", "is no longer based", "am not based", "is not based", "am no longer in", "is no longer in", "am not currently in", "is not currently in", "am currently not in", "is currently not in", "am not in", "is not in", "was in",
			"больше не жив", "уже не жив", "не жив", "раньше жил", "больше не прожива", "не прожива", "раньше прожива",
			"больше не нахож", "не нахож", "больше не находится", "не находится",
			"уже не в ", "больше не в ", "сейчас не в ", "не в ", "был в ", "была в ",
		} {
			if strings.HasPrefix(strings.ToLower(strings.ReplaceAll(tail, "’", "'")), negative) {
				if unrelatedLocationState(strings.TrimSpace(tail[len(negative):])) {
					return "", false, false
				}
				return "", false, true
			}
		}
		for _, marker := range []struct {
			text      string
			temporary bool
		}{
			{"am currently in ", true}, {"is currently in ", true}, {"сейчас нахожусь в ", true}, {"сейчас находится в ", true},
			{"нахожусь в ", true}, {"находится в ", true}, {"сейчас в ", true}, {"в ", true}, {"am in ", true}, {"is in ", true},
			{"live in ", false}, {"lives in ", false}, {"reside in ", false}, {"resides in ", false}, {"am based in ", false}, {"is based in ", false},
			{"живу в ", false}, {"живёт в ", false}, {"живет в ", false}, {"проживает в ", false}, {"проживаю в ", false},
		} {
			if len(tail) >= len(marker.text) && strings.EqualFold(tail[:len(marker.text)], marker.text) {
				candidate := tail[len(marker.text):]
				if unrelatedLocationState(candidate) {
					return "", false, false
				}
				if marker.text == "в " || marker.text == "am in " || marker.text == "is in " {
					first, _ := utf8.DecodeRuneInString(candidate)
					if unicode.IsLower(first) {
						return "", false, false
					}
				}
				return normalizeAssertedCity(safeCommandCity(candidate)), marker.temporary, true
			}
		}
	}
	for _, label := range []string{"My city: ", "My home city: ", "Мой город: ", "Мой город проживания: ", "Город проживания пользователя: "} {
		if len(text) >= len(label) && strings.EqualFold(text[:len(label)], label) {
			return safeCommandCity(text[len(label):]), false, true
		}
	}
	return "", false, false
}

// safeCommandCity accepts a short standalone city/country value, never the tail
// of an arbitrary memory record (which might contain instructions or secrets).
func safeCommandCity(raw string) string {
	city := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if city == "" || utf8.RuneCountInString(city) > 100 || strings.Count(city, ",") > 2 || strings.Contains(city, ". ") || security.RedactSensitiveText(city) != city || security.RedactPaymentData(city) != city {
		return ""
	}
	for _, r := range city {
		if !unicode.IsLetter(r) && !unicode.IsMark(r) && !strings.ContainsRune(" -–'’.,()", r) {
			return ""
		}
	}
	// A standalone proper place may have short lowercase particles (Rio de
	// Janeiro), but not a sentence following it. A single lowercase city is
	// also valid. This intentionally prefers an explicit command over guessing
	// from unrestricted prose in memory.
	for _, segment := range strings.Split(city, ",") {
		words := strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(segment))
		if len(words) == 0 || len(words) > 4 {
			return ""
		}
		if len(words) == 1 {
			continue
		}
		for i, word := range words {
			first, _ := utf8.DecodeRuneInString(word)
			if unicode.IsUpper(first) || unicode.ToUpper(first) == unicode.ToLower(first) {
				continue
			}
			particle := false
			if i > 0 && i < len(words)-1 {
				for _, allowed := range []string{"de", "del", "du", "la", "las", "los", "of", "the", "da", "do", "dos", "das", "am", "an", "im", "der", "den", "sur", "sous", "on", "upon", "di", "della", "a", "на"} {
					if word == allowed {
						particle = true
						break
					}
				}
			}
			if !particle {
				return ""
			}
		}
	}
	lower := strings.ToLower(city)
	for _, tail := range []string{" and ", " but ", " or ", " not ", " because ", " where ", " according ", " while ", " since ", " although ", " потому ", " поскольку ", " где ", " согласно ", " и ", " но ", " или ", " не ", " раньше ", " tomorrow", " завтра", "http", "token", "токен", "password", "пароль"} {
		if strings.Contains(lower, tail) {
			return ""
		}
	}
	// Preserve a disambiguating country hint, without replacing the city.
	if idx := strings.Index(city, "("); idx >= 0 {
		if !strings.HasSuffix(city, ")") || strings.Count(city, "(") != 1 || strings.Count(city, ")") != 1 {
			return ""
		}
		hint := strings.TrimSpace(city[idx+1 : len(city)-1])
		city = strings.TrimSpace(city[:idx])
		if city == "" || hint == "" {
			return ""
		}
		city += ", " + hint
	} else if strings.Contains(city, ")") {
		return ""
	}
	return city
}

var commandCoordinatePair = regexp.MustCompile(`^-?\d{1,3}\.\d{1,8}\s*,\s*-?\d{1,3}\.\d{1,8}$`)

// ValidateCommandLocation is the final narrow boundary before provider traffic,
// including explicit command arguments. Never forward arbitrary tails or markers.
func ValidateCommandLocation(raw string) (string, bool) {
	if len(raw) > 400 || strings.TrimSpace(raw) != raw {
		return "", false
	}
	if commandCoordinatePair.MatchString(raw) {
		if _, _, ok := parseCoordinatePair(raw); ok {
			return raw, true
		}
		return "", false
	}
	location := safeCommandCity(raw)
	return location, location != ""
}

func normalizeAssertedCity(city string) string {
	if city == "" {
		return ""
	}
	parts := strings.Split(city, ",")
	parts[0] = strings.TrimSpace(parts[0])
	irregular := map[string]string{"москве": "Москва", "вене": "Вена", "праге": "Прага", "риге": "Рига", "самаре": "Самара", "пензе": "Пенза", "берлине": "Берлин", "лондоне": "Лондон", "париже": "Париж", "санкт-петербурге": "Санкт-Петербург", "иркутске": "Иркутск", "казани": "Казань", "амстердаме": "Амстердам", "риме": "Рим", "стамбуле": "Стамбул", "мюнхене": "Мюнхен"}
	if normalized, ok := irregular[strings.ToLower(parts[0])]; ok {
		parts[0] = normalized
	}
	return strings.Join(parts, ",")
}

// A short 'in ...' phrase is often a state, not a place. These ordinary
// utterances must leave a previous real location untouched.
func unrelatedLocationState(raw string) bool {
	state := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(raw, ".")))
	for _, nonPlace := range []string{"порядке", "отпуске", "пути", "дороге", "командировке", "очереди", "процессе", "офисе", "гостях", "больнице", "магазине", "ресторане", "аэропорту", "trouble", "love", "bed", "a meeting", "a hurry", "the office", "the queue", "poverty"} {
		if state == nonPlace {
			return true
		}
	}
	return false
}
