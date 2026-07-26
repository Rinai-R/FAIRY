// Package reply owns compiled dialogue replies and their delivery timing contracts.
package reply

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInterrupted = errors.New("TURN_INTERRUPTED: companion turn was cancelled")

type ReplyChain struct {
	Text        string `json:"text"`
	SpeechText  string `json:"speechText"`
	VisualState string `json:"visualState"`
}

type SpeechSynthesisRequest struct {
	Text      string
	SpeakerID string
}

type SpeechSynthesisResult struct {
	SpeakerID string
	MimeType  string
	Format    string
	DataURL   string
}

type SpeechSynthesizer interface {
	SynthesizeSpeech(request SpeechSynthesisRequest) (SpeechSynthesisResult, error)
}

type BeatReadyCompletion struct {
	BeatID               string
	Kind                 string
	Index                uint8
	ChainIndex           int
	DisplayText          string
	SpeechText           string
	VisualState          string
	TargetIntervalMS     int64
	PaceWaitMS           int64
	PublishedPrefixCount int
	Reason               string
	Audio                *SpeechSynthesisResult
}

func ValidateReplyChains(chains []ReplyChain) error {
	if len(chains) == 0 {
		return errors.New("reply chains must contain at least one chain")
	}
	if len(chains) > MaxReplyChains {
		return fmt.Errorf("reply chains must contain at most %d chains", MaxReplyChains)
	}
	for i, chain := range chains {
		if strings.TrimSpace(chain.Text) == "" {
			return fmt.Errorf("reply chain %d text is required", i)
		}
		if strings.TrimSpace(chain.VisualState) == "" {
			return fmt.Errorf("reply chain %d visual_state is required", i)
		}
	}
	return nil
}
