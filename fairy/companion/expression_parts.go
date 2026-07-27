package companion

import (
	"fairy/memory"
	replyapp "fairy/reply"
	"fairy/session"
)

func memoryExpressionParts(chains []ReplyChain) []memory.ExpressionPart {
	parts := make([]memory.ExpressionPart, 0, len(chains))
	for _, chain := range chains {
		if chain.Kind == replyapp.ChainSticker && chain.Sticker != nil {
			parts = append(parts, memory.ExpressionPart{
				Kind:        memory.ExpressionSticker,
				VisualState: chain.VisualState,
				Sticker: &memory.StickerSnapshot{
					ID: chain.Sticker.ID, Description: chain.Sticker.Description, MIMEType: chain.Sticker.MIMEType,
				},
			})
			continue
		}
		parts = append(parts, memory.ExpressionPart{
			Kind: memory.ExpressionUtterance, Text: chain.Text, VisualState: chain.VisualState,
		})
	}
	return parts
}

func sessionExpressionPart(chain ReplyChain) session.ExpressionPart {
	if chain.Kind == replyapp.ChainSticker && chain.Sticker != nil {
		return session.ExpressionPart{
			Kind:        session.ExpressionSticker,
			VisualState: chain.VisualState,
			Sticker: &session.StickerReference{
				ID: chain.Sticker.ID, Description: chain.Sticker.Description, MIMEType: chain.Sticker.MIMEType,
			},
		}
	}
	return session.ExpressionPart{
		Kind: session.ExpressionUtterance, Text: chain.Text, VisualState: chain.VisualState,
	}
}
