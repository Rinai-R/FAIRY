package companion

import (
	"context"
	"errors"
	"testing"

	"fairy/memory"
)

type portraitMemory struct {
	portrait memory.RetrievalContext
	err      error
	calls    int
}

func (m *portraitMemory) RetrieveCharacterSocialMemoryContext(context.Context, string, string) (memory.SocialMemoryContext, error) {
	return memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{}}, nil
}

func (m *portraitMemory) CompanionPortraitContext(context.Context, string) (memory.RetrievalContext, error) {
	m.calls++
	return m.portrait, m.err
}

func TestRetrieveCompanionPortraitReadsOnlyPersonalInteractions(t *testing.T) {
	memoryPort := &portraitMemory{portrait: memory.RetrievalContext{PersonalMemories: []memory.RetrievedPersonalMemory{{ID: "preference-1", Kind: "preference", Content: "先陪伴再建议。"}}}}
	service := NewCompanionService()
	service.memory = memoryPorts{turn: turnMemoryPorts{portrait: memoryPort}}
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
