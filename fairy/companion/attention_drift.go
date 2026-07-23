package companion

import "strings"

const (
	DriftSubtle    = "subtle"
	DriftActive    = "active"
	DriftScattered = "scattered"
	DriftWild      = "wild"

	AnchorStrict   = "strict"
	AnchorBalanced = "balanced"
	AnchorLoose    = "loose"

	defaultDriftLevel    = DriftActive
	defaultAnchorPolicy  = AnchorBalanced
)

var driftLevelGuidance = map[string]string{
	DriftSubtle:    "轻微漂移：只在最近消息出现非常自然的触发点时轻轻联想一句；多数时候继续当前话题。",
	DriftActive:    "活跃联想：可以主动抓住新鲜、好笑、反差或熟悉细节接话，但仍要清楚、短促、能被最近消息解释。",
	DriftScattered: "明显发散：可被支线或反差点勾走，先接住再回到正题；允许一次可理解的突然拐弯。",
	DriftWild:      "强烈跳跃：可先被最有趣细节劫走一次插话或联想，但每轮最多一次明显跳跃，不能无视明确提问，结尾要看得出接的是哪条消息。",
}

var anchorPolicyGuidance = map[string]string{
	AnchorStrict:   "严格回钩：联想或短反应之后立刻回到当前主题或被回复对象。",
	AnchorBalanced: "自然回钩：可短暂沿支线一句，但结尾或主旨通常回到当前聊天。",
	AnchorLoose:    "宽松关联：可保留更自由的相关联想，但不能凭空换题，也不能无视明确提问。",
}

func normalizeDriftLevel(value string) string {
	switch strings.TrimSpace(value) {
	case DriftSubtle, DriftActive, DriftScattered, DriftWild:
		return strings.TrimSpace(value)
	default:
		return defaultDriftLevel
	}
}

func normalizeAnchorPolicy(value string) string {
	switch strings.TrimSpace(value) {
	case AnchorStrict, AnchorBalanced, AnchorLoose:
		return strings.TrimSpace(value)
	default:
		return defaultAnchorPolicy
	}
}

func validOptionalDriftLevel(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	_, ok := driftLevelGuidance[trimmed]
	return ok
}

func validOptionalAnchorPolicy(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	_, ok := anchorPolicyGuidance[trimmed]
	return ok
}

func attentionDriftGuidance(level, anchor string) (string, string) {
	level = normalizeDriftLevel(level)
	anchor = normalizeAnchorPolicy(anchor)
	return driftLevelGuidance[level], anchorPolicyGuidance[anchor]
}
