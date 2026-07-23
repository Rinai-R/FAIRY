package companion

const (
	SocialLearnMaxOutputTokens         uint32 = 2048
	socialLearningQueueCapacity               = 8
	socialLearningObservationThreshold        = 20
	maxSocialLearningEntries                  = 12
	maxSocialLearningSourceIDs                = 8
)

const SocialLearnInstructions = "Learn reusable public-group social context from the supplied external human observations. Output exactly one strict JSON object: {\"entries\":[{\"kind\":\"episode|expression|behavior\",\"situation\":\"<abstract situation>\",\"content\":\"<concise reusable summary or pattern>\",\"recallCue\":\"<natural-language retrieval cue>\",\"sourceMessageIds\":[\"<supporting message id>\"]}]}. The top level may contain only entries and each entry may contain only those five fields. Return between zero and 12 entries. Return an empty entries array when the evidence does not support a reusable pattern. Learn only from the supplied external human messages. Do not infer private facts, personality diagnoses, protected traits, secrets, or information not present in the evidence. episode captures public shared context, agreements, topic progress, or a group experience. expression captures a reusable situation-to-speaking-style pattern without copying a long original phrase. behavior captures a reusable situation-to-social-action-to-observed-outcome pattern only when the outcome is visible in later messages. Remove names, IDs, one-off proper nouns, screenshots, configuration values, and temporary task details from the learned content. Do not quote long source passages, imitate hostility more strongly than the evidence, or turn facts into personality claims. For every entry, sourceMessageIds must contain between one and eight unique message IDs from the supplied observations, and every referenced observation must directly support that entry. Do not output reasoning, prose, Markdown, unknown fields, null fields, or trailing data."
