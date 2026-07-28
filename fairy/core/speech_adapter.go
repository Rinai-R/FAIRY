package core

import (
	"errors"
	"fmt"
	"strings"

	"fairy/reply"
	"fairy/speech"
)

type companionSpeechAdapter struct {
	service *speech.SpeechService
}

func (a companionSpeechAdapter) SpeechReady() (bool, error) {
	if a.service == nil {
		return false, errors.New("speech service is unavailable")
	}
	status, err := a.service.Status()
	if err != nil {
		return false, fmt.Errorf("reading speech runtime readiness: %w", err)
	}
	return status.Configured && strings.TrimSpace(status.DefaultSpeaker) != "", nil
}

func (a companionSpeechAdapter) SynthesizeSpeech(request reply.SpeechSynthesisRequest) (reply.SpeechSynthesisResult, error) {
	result, err := a.service.SynthesizeSpeech(speech.SynthesizeSpeechRequest{Text: request.Text, SpeakerID: request.SpeakerID})
	if err != nil {
		return reply.SpeechSynthesisResult{}, err
	}
	return reply.SpeechSynthesisResult{
		SpeakerID: result.SpeakerID,
		MimeType:  result.MimeType,
		Format:    result.Format,
		DataURL:   result.DataURL,
	}, nil
}
