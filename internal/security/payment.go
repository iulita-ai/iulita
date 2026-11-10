package security

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
)

// TextEntity offsets use Telegram's UTF-16 code units, not bytes or runes.
type TextEntity struct {
	Type           string
	Offset, Length int
}
type paymentSpan struct {
	start, end  int
	replacement string
}

var cardCandidate = regexp.MustCompile(`\d(?:[ -]?\d){12,18}`)
var tonCandidate = regexp.MustCompile(`(?:[EU]Q[A-Za-z0-9_-]{46}|-?0:[A-Fa-f0-9]{64})`)

func cardMask(s string) string {
	var digits strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if len(d) < 4 {
		return "[CARD]"
	}
	return "•••• " + d[len(d)-4:]
}
func luhn(s string) bool {
	var d []int
	for _, r := range s {
		if r >= '0' && r <= '9' {
			d = append(d, int(r-'0'))
		}
	}
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	total := 0
	parity := len(d) % 2
	for i, n := range d {
		if i%2 == parity {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		total += n
	}
	return total%10 == 0
}
func applySpans(text string, spans []paymentSpan) string {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end > spans[j].end
		}
		return spans[i].start < spans[j].start
	})
	var out strings.Builder
	end := 0
	for _, s := range spans {
		if s.start < end || s.start < 0 || s.end > len(text) || s.end <= s.start {
			continue
		}
		out.WriteString(text[end:s.start])
		out.WriteString(s.replacement)
		end = s.end
	}
	out.WriteString(text[end:])
	return out.String()
}

// RedactPaymentData is a best-effort fallback for unannotated text. It does
// not claim to discover all financial data (for example OCR or obfuscation).
func RedactPaymentData(text string) string {
	var spans []paymentSpan
	for _, m := range cardCandidate.FindAllStringIndex(text, -1) {
		if (m[0] > 0 && text[m[0]-1] >= '0' && text[m[0]-1] <= '9') || (m[1] < len(text) && text[m[1]] >= '0' && text[m[1]] <= '9') {
			continue
		}
		s := text[m[0]:m[1]]
		if luhn(s) {
			spans = append(spans, paymentSpan{m[0], m[1], cardMask(s)})
		}
	}
	for _, m := range tonCandidate.FindAllStringIndex(text, -1) {
		spans = append(spans, paymentSpan{m[0], m[1], "[TON ADDRESS]"})
	}
	return applySpans(text, spans)
}

// RedactPaymentEntities also honors explicitly annotated numbers that do not
// pass Luhn. Malformed/split-surrogate offsets cannot select arbitrary bytes.
func RedactPaymentEntities(text string, entities []TextEntity) string {
	boundaries := map[int]int{0: 0}
	units := 0
	for pos, r := range text {
		boundaries[units] = pos
		units += len(utf16.Encode([]rune{r}))
		boundaries[units] = pos + len(string(r))
	}
	var spans []paymentSpan
	for _, e := range entities {
		if e.Type != "bank_card_number" && e.Type != "ton_address" {
			continue
		}
		if e.Offset < 0 || e.Length <= 0 || e.Offset > units || e.Length > units-e.Offset {
			continue
		}
		a, ok := boundaries[e.Offset]
		b, ok2 := boundaries[e.Offset+e.Length]
		if !ok || !ok2 {
			continue
		}
		replacement := "[TON ADDRESS]"
		if e.Type == "bank_card_number" {
			replacement = cardMask(text[a:b])
		}
		spans = append(spans, paymentSpan{a, b, replacement})
	}
	return RedactPaymentData(applySpans(text, spans))
}

// RedactSensitiveText is for persistence and model context, never a silent
// rewrite of a response whose contract promises verbatim delivery.
func RedactSensitiveText(text string) string {
	text = RedactPaymentData(text)
	for _, p := range secretPatterns {
		text = p.re.ReplaceAllString(text, "[REDACTED]")
	}
	return text
}
