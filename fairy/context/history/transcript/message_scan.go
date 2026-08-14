package transcript

import (
	"encoding/json"
	"errors"
	"fmt"

	historyexpr "fairy/context/history/expression"
	"fairy/transport/session"
)

type EndpointConversationRow struct {
	ConversationID     string
	Audience           string
	Initiation         string
	Presentation       string
	PrincipalNamespace string
	PrincipalDigest    string
	Evaluation         bool
}

func (row EndpointConversationRow) Binding(endpoint session.EndpointKind) session.Binding {
	return session.Binding{
		Endpoint: endpoint,
		Facts: session.Facts{
			Audience:           session.AudienceKind(row.Audience),
			Initiation:         session.InitiationKind(row.Initiation),
			Presentation:       session.PresentationKind(row.Presentation),
			PrincipalNamespace: row.PrincipalNamespace,
			PrincipalDigest:    row.PrincipalDigest,
			Evaluation:         row.Evaluation,
		},
	}
}

func validateEndpointConversationKey(characterID string, binding session.Binding, digest string) error {
	if err := ValidateID("character_id", characterID); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if err := session.ValidateDigest(digest); err != nil {
		return errors.New("endpoint key digest is invalid")
	}
	return nil
}

func finishScannedMessage(message MessageRecord, sequence int64, expressionPartsJSON []byte) (MessageRecord, error) {
	if err := json.Unmarshal(expressionPartsJSON, &message.Parts); err != nil {
		return MessageRecord{}, fmt.Errorf("decoding conversation message expression parts: %w", err)
	}
	if message.Parts == nil {
		message.Parts = []historyexpr.Part{}
	}
	if message.Role == "assistant" && len(message.Parts) > 0 {
		if err := validateExpressionMessage(message.Content, message.Parts); err != nil {
			return MessageRecord{}, fmt.Errorf("validating conversation message expression parts: %w", err)
		}
	}
	message.Sequence = uint64(sequence)
	return message, nil
}
