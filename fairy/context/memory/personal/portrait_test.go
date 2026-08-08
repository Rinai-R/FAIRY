package personal

import (
	"strings"
	"testing"
)

func TestBuildCompanionPortraitIsBoundedAndScopeSafe(t *testing.T) {
	records := []Record{
		{ID: "profile-new", Kind: "profile", Scope: Scope{Type: "global"}, Content: "用户说话直接，但不喜欢被催促。", ConfidenceBasisPoints: 9000, UpdatedAtUnixMS: 9},
		{ID: "profile-old", Kind: "profile", Scope: Scope{Type: "global"}, Content: "用户更喜欢短句。", ConfidenceBasisPoints: 8000, UpdatedAtUnixMS: 8},
		{ID: "profile-third", Kind: "profile", Scope: Scope{Type: "global"}, Content: "不应超过每 kind 上限。", ConfidenceBasisPoints: 7000, UpdatedAtUnixMS: 7},
		{ID: "preference-1", Kind: "preference", Scope: Scope{Type: "global"}, Content: "情绪低落时先陪伴，不急着给建议。", ConfidenceBasisPoints: 9200, UpdatedAtUnixMS: 10},
		{ID: "relationship-1", Kind: "relationship", Scope: Scope{Type: "character", CharacterID: "character-1"}, Content: "已经建立稳定信任，可以自然接住沉默。", ConfidenceBasisPoints: 9100, UpdatedAtUnixMS: 11},
		{ID: "relationship-foreign", Kind: "relationship", Scope: Scope{Type: "character", CharacterID: "character-2"}, Content: "其他角色关系不得进入。", ConfidenceBasisPoints: 10000, UpdatedAtUnixMS: 12},
		{ID: "legacy", Kind: "relationship", Scope: Scope{Type: "unassigned_legacy"}, Content: "不得进入画像。", ConfidenceBasisPoints: 10000},
	}
	portrait, err := buildCompanionPortrait("character-1", records)
	if err != nil {
		t.Fatal(err)
	}
	if len(portrait.PersonalMemories) != 4 {
		t.Fatalf("portrait memories = %#v", portrait.PersonalMemories)
	}
	joined := ""
	for _, item := range portrait.PersonalMemories {
		joined += item.ID + ":" + item.Content + "\n"
	}
	for _, forbidden := range []string{"profile-third", "legacy", "不得进入画像", "relationship-foreign", "其他角色关系"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("portrait leaked %q: %s", forbidden, joined)
		}
	}
}

func TestBuildCompanionPortraitKeepsKindDiversityBeforeSecondItems(t *testing.T) {
	records := make([]Record, 0, 8)
	for _, kind := range []string{"profile", "preference", "relationship", "experience"} {
		for index := range 2 {
			scope := Scope{Type: "global"}
			if kind == "relationship" {
				scope = Scope{Type: "character", CharacterID: "character-1"}
			}
			records = append(records, Record{
				ID: kind + string(rune('a'+index)), Kind: kind, Scope: scope,
				Content: kind + " memory", ConfidenceBasisPoints: uint16(9000 - index),
			})
		}
	}
	portrait, err := buildCompanionPortrait("character-1", records)
	if err != nil {
		t.Fatal(err)
	}
	if len(portrait.PersonalMemories) != maxPortraitMemories {
		t.Fatalf("portrait size = %d", len(portrait.PersonalMemories))
	}
	kinds := make(map[string]int, 4)
	for _, item := range portrait.PersonalMemories {
		kinds[item.Kind]++
	}
	for _, kind := range []string{"profile", "preference", "relationship", "experience"} {
		if kinds[kind] == 0 {
			t.Fatalf("portrait starved %q: %#v", kind, portrait.PersonalMemories)
		}
	}
}
