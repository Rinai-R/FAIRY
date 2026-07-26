package memory

import "unicode"

type extractionProjectionField struct {
	fragments [][]rune
	next      int
}

// BuildExtractionRetrievalProjection builds a bounded FTS projection from turn evidence.
func BuildExtractionRetrievalProjection(turns []ExtractionTurn) []string {
	fields := make([]extractionProjectionField, 0, len(turns)*2)
	for _, turn := range turns {
		for _, value := range []string{turn.UserMessage, turn.AssistantMessage} {
			fragments := extractionSearchFragments(value)
			if len(fragments) > 0 {
				fields = append(fields, extractionProjectionField{fragments: fragments})
			}
		}
	}
	if len(fields) == 0 {
		return []string{}
	}

	projection := make([]string, 0, len(fields))
	remaining := MaxFTSQueryChars
	for remaining >= 3 {
		advanced := false
		for index := range fields {
			field := &fields[index]
			if field.next >= len(field.fragments) {
				continue
			}
			advanced = true
			fragment := field.fragments[field.next]
			field.next++

			separatorRunes := 0
			if len(projection) > 0 {
				separatorRunes = 1
			}
			available := remaining - separatorRunes
			if available < 3 {
				return projection
			}
			if len(fragment) > available {
				fragment = fragment[:available]
			}
			projection = append(projection, string(fragment))
			remaining -= separatorRunes + len(fragment)
			if remaining < 3 {
				return projection
			}
		}
		if !advanced {
			break
		}
	}
	return projection
}

func extractionSearchFragments(value string) [][]rune {
	fragments := make([][]rune, 0)
	run := make([]rune, 0)
	flush := func() {
		if len(run) < 3 {
			run = run[:0]
			return
		}
		for len(run) > extractionProjectionFragmentRunes {
			if len(run)-extractionProjectionFragmentRunes < 3 {
				break
			}
			fragment := append([]rune(nil), run[:extractionProjectionFragmentRunes]...)
			fragments = append(fragments, fragment)
			run = run[extractionProjectionFragmentRunes:]
		}
		fragments = append(fragments, append([]rune(nil), run...))
		run = run[:0]
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			run = append(run, character)
			continue
		}
		flush()
	}
	flush()
	return fragments
}
