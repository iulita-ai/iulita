package translation

// SourceLanguages returns a fresh list of documented agent codes; these are
// independent of Iulita's UI locale catalog. Pinyin/IPA are outside this skill.
func SourceLanguages() []string {
	return []string{"auto", "zh-CN", "zh-TW", "wyw", "yue", "en", "ja", "ko", "fr", "de", "es", "ru", "pt", "it", "ar", "hi", "bg", "cs", "da", "el", "et", "fi", "hu", "id", "lt", "lv", "nl", "no", "pl", "ro", "sk", "sl", "sv", "th", "tr", "uk", "vi", "my", "ms"}
}

// TargetLanguages includes documented English variants and excludes autodetection.
func TargetLanguages() []string {
	return append(SourceLanguages()[1:], "en-GB", "en-US")
}
