//go:build !endpointstrict

package session

var configSections = map[string]struct{}{
	"model": {}, "web-search": {}, "semantic-embedding": {}, "qq-onebot": {},
}
