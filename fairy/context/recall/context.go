// Package recall defines the bounded cross-domain context assembled for one
// model/tool call. Domain stores keep their own retrieval result types; Agent
// orchestration is responsible for merging them into Context.
package recall

import (
	"fairy/context/knowledge"
	"fairy/context/memory/personal"
	"fairy/context/social"
)

type Context struct {
	PersonalMemories []personal.Retrieved       `json:"personalMemories"`
	Knowledge        []knowledge.Retrieved      `json:"knowledge"`
	SocialMemories   social.SocialMemoryContext `json:"socialMemories,omitempty"`
	SemanticStatus   string                     `json:"semanticStatus,omitempty"`
}

func (c Context) Empty() bool {
	return len(c.PersonalMemories) == 0 && len(c.Knowledge) == 0 && c.SocialMemories.Empty()
}
