package presence

import "fairy/context/character"

const (
	DriftSubtle         = character.DriftSubtle
	DriftActive         = character.DriftActive
	DriftScattered      = character.DriftScattered
	DriftWild           = character.DriftWild
	AnchorStrict        = character.AnchorStrict
	AnchorBalanced      = character.AnchorBalanced
	AnchorLoose         = character.AnchorLoose
	DefaultDriftLevel   = character.DefaultDriftLevel
	DefaultAnchorPolicy = character.DefaultAnchorPolicy
)

func NormalizeDriftLevel(value string) string {
	return character.NormalizeDriftLevel(value)
}

func NormalizeAnchorPolicy(value string) string {
	return character.NormalizeAnchorPolicy(value)
}

func ValidOptionalDriftLevel(value string) bool {
	return character.ValidOptionalDriftLevel(value)
}

func ValidOptionalAnchorPolicy(value string) bool {
	return character.ValidOptionalAnchorPolicy(value)
}

func AttentionDriftGuidance(level, anchor string) (string, string) {
	return character.AttentionDriftGuidance(level, anchor)
}
