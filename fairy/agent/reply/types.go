// Package reply owns compiled dialogue replies and their delivery timing contracts.
package reply

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInterrupted = errors.New("TURN_INTERRUPTED: companion turn was cancelled")

type ChainKind string

const (
	ChainUtterance      ChainKind = "utterance"
	ChainSticker        ChainKind = "sticker"
	BeatKindUtterance             = "utterance"
	BeatKindFinal                 = "final"
	ChainIndexUtterance           = -1
)

type StickerReference struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType"`
}

type ReplyChain struct {
	Kind        ChainKind         `json:"kind,omitempty"`
	Text        string            `json:"text,omitempty"`
	VisualState string            `json:"visualState"`
	Sticker     *StickerReference `json:"sticker,omitempty"`
}

type BeatReadyCompletion struct {
	BeatID               string
	Kind                 string
	Index                uint8
	ChainIndex           int
	DisplayText          string
	VisualState          string
	TargetIntervalMS     int64
	PaceWaitMS           int64
	PublishedPrefixCount int
	Reason               string
	ReplyTargetMessageID string
	Chain                *ReplyChain
}

func ValidateReplyChains(chains []ReplyChain) error {
	if len(chains) == 0 {
		return errors.New("reply chains must contain at least one chain")
	}
	if len(chains) > MaxReplyChains {
		return fmt.Errorf("reply chains must contain at most %d chains", MaxReplyChains)
	}
	stickerCount := 0
	for i, chain := range chains {
		if strings.TrimSpace(chain.VisualState) == "" {
			return fmt.Errorf("reply chain %d visual_state is required", i)
		}
		switch chain.Kind {
		case "", ChainUtterance:
			if strings.TrimSpace(chain.Text) == "" {
				return fmt.Errorf("reply chain %d text is required", i)
			}
			if chain.Sticker != nil {
				return fmt.Errorf("reply chain %d utterance must not contain sticker", i)
			}
		case ChainSticker:
			stickerCount++
			if stickerCount > 1 {
				return errors.New("reply chains must contain at most one sticker")
			}
			if chain.Sticker == nil || strings.TrimSpace(chain.Sticker.ID) == "" ||
				strings.TrimSpace(chain.Sticker.Description) == "" || strings.TrimSpace(chain.Sticker.MIMEType) == "" {
				return fmt.Errorf("reply chain %d sticker snapshot is required", i)
			}
			if chain.Text != "" {
				return fmt.Errorf("reply chain %d sticker must not contain text", i)
			}
		default:
			return fmt.Errorf("reply chain %d kind %q is invalid", i, chain.Kind)
		}
	}
	return nil
}
