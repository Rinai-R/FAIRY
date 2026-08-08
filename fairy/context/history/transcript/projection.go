package transcript

import (
	historyprojection "fairy/context/history/projection"
)

func applyPromptProjection(messages []MessageRecord, projection historyprojection.State) []MessageRecord {
	if len(messages) == 0 || len(projection.Omissions) == 0 {
		return messages
	}
	filtered := make([]MessageRecord, 0, len(messages))
	for _, message := range messages {
		omitted := false
		for _, omission := range projection.Omissions {
			if omission.StartMessageSequence > 0 &&
				message.Sequence >= omission.StartMessageSequence &&
				message.Sequence <= omission.EndMessageSequence {
				omitted = true
				break
			}
		}
		if !omitted {
			filtered = append(filtered, message)
		}
	}
	return filtered
}
