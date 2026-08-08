package conversation

import (
	"fairy/agent/reply"
	historyexpr "fairy/context/history/expression"
	"fairy/transport/session"
)

func memoryExpressionParts(chains []reply.ReplyChain) []historyexpr.Part {
	parts := make([]historyexpr.Part, 0, len(chains))
	for _, chain := range chains {
		if chain.Kind == reply.ChainSticker && chain.Sticker != nil {
			parts = append(parts, historyexpr.Part{
				Kind:        historyexpr.Sticker,
				VisualState: chain.VisualState,
				Sticker: &historyexpr.StickerSnapshot{
					ID: chain.Sticker.ID, Description: chain.Sticker.Description, MIMEType: chain.Sticker.MIMEType,
				},
			})
			continue
		}
		parts = append(parts, historyexpr.Part{
			Kind: historyexpr.Utterance, Text: chain.Text, VisualState: chain.VisualState,
		})
	}
	return parts
}

func sessionExpressionPart(chain reply.ReplyChain) session.ExpressionPart {
	if chain.Kind == reply.ChainSticker && chain.Sticker != nil {
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
