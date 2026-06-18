package app

import "testing"

func TestNormalizeLanguageCodeAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "cn", input: "cn", want: "zh-CN"},
		{name: "zh", input: "zh", want: "zh-CN"},
		{name: "zh-CN", input: "zh-CN", want: "zh-CN"},
		{name: "zh underscore", input: "zh_CN", want: "zh-CN"},
		{name: "jp", input: "jp", want: "ja-JP"},
		{name: "ja", input: "ja", want: "ja-JP"},
		{name: "ja-JP", input: "ja-JP", want: "ja-JP"},
		{name: "en", input: "en", want: "en-US"},
		{name: "en-US", input: "en-US", want: "en-US"},
		{name: "unknown preserved", input: "ko-KR", want: "ko-KR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeLanguageCode(tt.input); got != tt.want {
				t.Fatalf("NormalizeLanguageCode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLanguagePlanNormalizeDefaultsAndSameMode(t *testing.T) {
	t.Parallel()

	got := (LanguagePlan{
		DisplayLanguage:     "cn",
		SpeechLanguage:      "jp",
		TranslationProvider: "",
		Mode:                "",
	}).Normalize()
	if got.DisplayLanguage != "zh-CN" {
		t.Fatalf("DisplayLanguage = %q, want zh-CN", got.DisplayLanguage)
	}
	if got.SpeechLanguage != "ja-JP" {
		t.Fatalf("SpeechLanguage = %q, want ja-JP", got.SpeechLanguage)
	}
	if got.TranslationProvider != DefaultTranslationProvider {
		t.Fatalf("TranslationProvider = %q, want %q", got.TranslationProvider, DefaultTranslationProvider)
	}
	if got.Mode != DefaultLanguageMode {
		t.Fatalf("Mode = %q, want %q", got.Mode, DefaultLanguageMode)
	}

	same := (LanguagePlan{DisplayLanguage: "ja", SpeechLanguage: "zh", Mode: "same"}).Normalize()
	if same.DisplayLanguage != "ja-JP" || same.SpeechLanguage != "ja-JP" {
		t.Fatalf("same mode language = display %q speech %q, want both ja-JP", same.DisplayLanguage, same.SpeechLanguage)
	}
}
