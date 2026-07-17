package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func runtimeHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
