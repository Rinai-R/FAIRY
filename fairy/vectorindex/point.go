// Package vectorindex owns the Qdrant collection and point contract.
package vectorindex

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

const (
	ItemKindPersonalMemory = "personal_memory"
	ItemKindKnowledge      = "knowledge"
)

var memoryPointNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://fairy.local/vectorindex/memory/v1"))

func PointID(itemKind, itemID, modelID string) (uuid.UUID, error) {
	if itemKind != ItemKindPersonalMemory && itemKind != ItemKindKnowledge {
		return uuid.Nil, errors.New("vector item kind is unsupported")
	}
	if !validPointToken(itemID) {
		return uuid.Nil, errors.New("vector item id is invalid")
	}
	if !validPointToken(modelID) {
		return uuid.Nil, errors.New("vector model id is invalid")
	}
	name := itemKind + "\x00" + itemID + "\x00" + modelID
	return uuid.NewSHA1(memoryPointNamespace, []byte(name)), nil
}

func validPointToken(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00')
}
