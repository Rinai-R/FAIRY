package retrieval

import (
	"errors"
	"sort"
	"strings"
	"unicode"
)

const MaxQueryRunes = 2000

func NormalizePostgresQuery(query string) (string, error) {
	usable, err := BuildFTSQuery(query)
	if err != nil {
		return "", err
	}
	if usable == "" {
		return "", nil
	}
	return strings.Join(strings.Fields(query), " "), nil
}

func BuildFTSQuery(query string) (string, error) {
	if len([]rune(query)) > MaxQueryRunes {
		return "", errors.New("retrieval query is too long or contains control characters")
	}
	for _, character := range query {
		if unicode.IsControl(character) {
			return "", errors.New("retrieval query is too long or contains control characters")
		}
	}
	terms := make(map[string]struct{})
	chunk := make([]rune, 0)
	flush := func() {
		if len(chunk) >= 3 {
			for index := 0; index+3 <= len(chunk); index++ {
				terms[string(chunk[index:index+3])] = struct{}{}
			}
		}
		chunk = chunk[:0]
	}
	for _, character := range query {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			chunk = append(chunk, character)
			continue
		}
		flush()
	}
	flush()
	if len(terms) == 0 {
		return "", nil
	}
	ordered := make([]string, 0, len(terms))
	for term := range terms {
		ordered = append(ordered, term)
	}
	sort.Strings(ordered)
	quoted := make([]string, 0, len(ordered))
	for _, term := range ordered {
		quoted = append(quoted, `"`+term+`"`)
	}
	return strings.Join(quoted, " OR "), nil
}
