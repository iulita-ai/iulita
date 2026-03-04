package assistant

import "testing"

func FuzzSanitizeFTSQuery(f *testing.F) {
	f.Add("hello world")
	f.Add("AND OR NOT NEAR")
	f.Add(`"quoted" (grouped) {braced}`)
	f.Add("*wildcard* ^prefix")
	f.Add("привет мир запомни")
	f.Add("")
	f.Add("a")
	f.Add(`foo AND "bar" OR NOT (baz NEAR qux)`)
	f.Add("!@#$%^&*()_+-=[]{}|;':\",./<>?")

	f.Fuzz(func(t *testing.T, input string) {
		// Should never panic.
		result := sanitizeFTSQuery(input)
		_ = result
	})
}
