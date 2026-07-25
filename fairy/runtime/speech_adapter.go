package runtime

import (
	"fairy/internal/app/reply"
	"fairy/speech"
)

type companionSpeechAdapter struct {
	service *speech.SpeechService
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
