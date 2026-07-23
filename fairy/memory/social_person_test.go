package memory

import (
	"strings"
	"testing"
)

func TestValidateSocialPersonNoteInput(t *testing.T) {
	valid := SocialPersonNoteInput{
		CharacterID: "character-1", ConversationID: "conversation-1", SenderID: "u1",
		SenderName: "甲", Note: "常吐槽但会接话",
	}
	if err := validateSocialPersonNoteInput(valid); err != nil {
		t.Fatal(err)
	}
	empty := valid
	empty.Note = ""
	if err := validateSocialPersonNoteInput(empty); err == nil {
		t.Fatal("empty note accepted")
	}
	oversized := valid
	oversized.Note = strings.Repeat("话", MaxSocialPersonNoteRunes+1)
	if err := validateSocialPersonNoteInput(oversized); err == nil {
		t.Fatal("oversized note accepted")
	}
}
