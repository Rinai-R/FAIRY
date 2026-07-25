package participation

import "fairy/internal/domain/persona"

const (
	DriftSubtle         = persona.DriftSubtle
	DriftActive         = persona.DriftActive
	DriftScattered      = persona.DriftScattered
	DriftWild           = persona.DriftWild
	AnchorStrict        = persona.AnchorStrict
	AnchorBalanced      = persona.AnchorBalanced
	AnchorLoose         = persona.AnchorLoose
	DefaultDriftLevel   = persona.DefaultDriftLevel
	DefaultAnchorPolicy = persona.DefaultAnchorPolicy
)

func NormalizeDriftLevel(value string) string {
	return persona.NormalizeDriftLevel(value)
}

func NormalizeAnchorPolicy(value string) string {
	return persona.NormalizeAnchorPolicy(value)
}

func ValidOptionalDriftLevel(value string) bool {
	return persona.ValidOptionalDriftLevel(value)
}

func ValidOptionalAnchorPolicy(value string) bool {
	return persona.ValidOptionalAnchorPolicy(value)
}

func AttentionDriftGuidance(level, anchor string) (string, string) {
	return persona.AttentionDriftGuidance(level, anchor)
}
