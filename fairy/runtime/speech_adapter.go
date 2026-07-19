package runtime

import (
	"fairy/companion"
	"fairy/speech"
)

type companionSpeechAdapter struct {
	service *speech.SpeechService
}

func (a companionSpeechAdapter) SynthesizeSpeech(request companion.SpeechSynthesisRequest) (companion.SpeechSynthesisResult, error) {
	result, err := a.service.SynthesizeSpeech(speech.SynthesizeSpeechRequest{Text: request.Text, SpeakerID: request.SpeakerID})
	if err != nil {
		return companion.SpeechSynthesisResult{}, err
	}
	return companion.SpeechSynthesisResult{
		SpeakerID: result.SpeakerID,
		MimeType:  result.MimeType,
		Format:    result.Format,
		DataURL:   result.DataURL,
	}, nil
}
