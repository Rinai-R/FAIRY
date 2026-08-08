package extraction

import (
	"strings"
	"unicode"

	memoryretrieval "fairy/context/memory/retrieval"
)

const projectionFragmentRunes = 64

// BuildRetrievalProjection derives bounded search hints without altering the
// complete turn evidence passed to the learning model.
func BuildRetrievalProjection(turns []Turn) []string {
	fragments := make([]string, 0, len(turns)*2)
	remaining := memoryretrieval.MaxQueryRunes
	for _, turn := range turns {
		for _, field := range []string{turn.UserMessage, turn.AssistantMessage} {
			for _, token := range strings.FieldsFunc(field, unicode.IsSpace) {
				token = strings.TrimSpace(token)
				runes := []rune(token)
				if len(runes) > projectionFragmentRunes {
					runes = runes[:projectionFragmentRunes]
				}
				if len(runes) > remaining {
					runes = runes[:remaining]
				}
				fragment := string(runes)
				if fragment == "" {
					continue
				}
				usable, err := memoryretrieval.BuildFTSQuery(fragment)
				if err != nil || usable == "" {
					continue
				}
				fragments = append(fragments, fragment)
				remaining -= len(runes)
				break
			}
			if remaining == 0 {
				return fragments
			}
		}
	}
	return fragments
}
