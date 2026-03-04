package skillmgr

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// maxArchiveSize is the maximum allowed archive size (50 MB).
const maxArchiveSize = 50 << 20

// VerifyChecksum computes SHA256 of a file and compares against expected.
// If expected is empty, returns the computed checksum without error.
func VerifyChecksum(path, expected string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxArchiveSize+1)); err != nil {
		return "", fmt.Errorf("compute checksum: %w", err)
	}

	computed := fmt.Sprintf("%x", h.Sum(nil))
	if expected != "" && computed != expected {
		return computed, fmt.Errorf("checksum mismatch: got %s, want %s", computed, expected)
	}

	return computed, nil
}

// injectionPatterns are regex patterns that indicate possible prompt injection.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?previous\s+instructions`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+(a\s+)?`),
	regexp.MustCompile(`(?i)forget\s+(all\s+)?(your\s+)?instructions`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?prior`),
	regexp.MustCompile(`(?i)system\s*:\s*you\s+are`),
	regexp.MustCompile(`(?i)do\s+anything\s+now`), // DAN-style
	regexp.MustCompile(`(?i)\bDAN\b.*\bmode\b`),
	regexp.MustCompile(`(?i)jailbreak`),
}

// scanForInjection checks markdown body for prompt injection patterns.
// Returns a list of warning strings (empty = clean).
func scanForInjection(body string) []string {
	var warnings []string
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		for _, pat := range injectionPatterns {
			if pat.MatchString(line) {
				warnings = append(warnings, fmt.Sprintf("line %d: possible prompt injection pattern: %s", i+1, pat.String()))
			}
		}
	}
	return warnings
}
