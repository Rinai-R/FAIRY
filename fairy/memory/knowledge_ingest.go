package memory

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

func acceptKnowledgeIngest(category, topic, statement, sourceURL string, rank uint8) bool {
	switch strings.TrimSpace(category) {
	case "anime", "game", "book":
	default:
		return false
	}
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if topic == "" || statement == "" || rank < 1 || rank > 5 {
		return false
	}
	if utf8.RuneCountInString(statement) < 8 {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	return true
}
