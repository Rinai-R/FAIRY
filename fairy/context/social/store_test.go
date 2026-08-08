package social

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSocialMemoryContentHashIsStableAndKindSeparated(t *testing.T) {
	entry := SocialMemoryEntryInput{
		Kind: SocialMemoryExpression, Situation: "群友用反讽方式夸张吐槽时",
		Content: "用一小句顺着反讽接话，不解释梗", RecallCue: "轻松群聊中的反讽和抽象梗",
		SourceStartUnixMS: 10, SourceEndUnixMS: 20,
	}
	first := SocialMemoryContentHash(entry)
	entry.Content = "用一小句顺着反讽接话，不解释梗"
	if second := SocialMemoryContentHash(entry); second != first {
		t.Fatalf("stable hash changed: %s != %s", second, first)
	}
	entry.Kind = SocialMemoryBehavior
	if SocialMemoryContentHash(entry) == first {
		t.Fatal("different social memory kinds shared a hash")
	}
}

func TestBuildSocialRetrievalProjectionIsBoundedAndDeterministic(t *testing.T) {
	query := "我最近有点实习焦虑"
	first := buildSocialRetrievalProjection(query)
	second := buildSocialRetrievalProjection(query)
	if strings.Join(first, "|") != strings.Join(second, "|") {
		t.Fatalf("projection is not deterministic: %q != %q", first, second)
	}
	if len(first) == 0 || len(first) > maxSocialQueryFragments {
		t.Fatalf("projection fragment count = %d", len(first))
	}
	if !containsString(first, "实习焦虑") {
		t.Fatalf("projection %q does not preserve the retrieval topic", first)
	}
	total := 0
	for _, fragment := range first {
		total += utf8.RuneCountInString(fragment)
	}
	if total > maxSocialQueryRunes {
		t.Fatalf("projection rune count = %d, want <= %d", total, maxSocialQueryRunes)
	}
}

func TestBuildSocialRetrievalProjectionSamplesLongQueryAcrossItsRange(t *testing.T) {
	projection := buildSocialRetrievalProjection(strings.Repeat("前", MaxFTSQueryChars-4) + "结尾主题")
	if len(projection) > maxSocialQueryFragments {
		t.Fatalf("projection fragment count = %d", len(projection))
	}
	if !containsString(projection, "结尾主题") {
		t.Fatalf("projection %q does not cover the query tail", projection)
	}
}

func TestBuildSocialRetrievalProjectionRejectsQueriesWithoutTrigrams(t *testing.T) {
	for _, query := range []string{"", " ", "!?", "ab"} {
		if projection := buildSocialRetrievalProjection(query); len(projection) != 0 {
			t.Fatalf("buildSocialRetrievalProjection(%q) = %q, want empty", query, projection)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
