package conversation

import (
	"context"
	"errors"
	"testing"

	"fairy/context/memory/personal"
	"fairy/context/social"
)

type portraitMemory struct {
	portrait personal.Retrieval
	err      error
	calls    int
}

func (m *portraitMemory) RetrieveCharacterSocialMemoryContext(context.Context, string, string) (social.SocialMemoryContext, error) {
	return social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{}}, nil
}

func (m *portraitMemory) RetrieveSocialMemoryContext(context.Context, string, string, string) (social.SocialMemoryContext, error) {
	return social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{}}, nil
}

func (m *portraitMemory) CompanionPortraitContext(context.Context, string) (personal.Retrieval, error) {
	m.calls++
	return m.portrait, m.err
}

func TestRetrieveCompanionPortraitReadsOnlyPersonalInteractions(t *testing.T) {
	memoryPort := &portraitMemory{portrait: personal.Retrieval{PersonalMemories: []personal.Retrieved{{ID: "preference-1", Kind: "preference", Content: "先陪伴再建议。"}}}}
	service := NewService()
	service.memory = memoryPorts{
		turn:    turnMemoryPorts{portrait: memoryPort},
		ambient: ambientMemoryPorts{socialRetrieval: memoryPort},
	}
	portrait, err := service.retrieveCompanionPortrait(t.Context(), "character-1", "安静音乐", desktopResolved())
	if err != nil || len(portrait.PersonalMemories) != 1 || memoryPort.calls != 1 {
		t.Fatalf("private portrait = %#v, calls=%d, error=%v", portrait, memoryPort.calls, err)
	}
	public, err := service.retrieveCompanionPortrait(t.Context(), "character-1", "安静音乐", publicAmbientResolved())
	if err != nil || !public.Empty() || memoryPort.calls != 1 {
		t.Fatalf("public portrait = %#v, calls=%d, error=%v", public, memoryPort.calls, err)
	}
	memoryPort.err = errors.New("database failed")
	if _, err := service.retrieveCompanionPortrait(t.Context(), "character-1", "安静音乐", desktopResolved()); err == nil {
		t.Fatal("portrait storage error was hidden")
	}
}
