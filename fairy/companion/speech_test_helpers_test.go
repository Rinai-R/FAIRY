package companion

import "sync"

type recordingSynth struct {
	mu    sync.Mutex
	texts []string
}

func (r *recordingSynth) SynthesizeSpeech(request SpeechSynthesisRequest) (SpeechSynthesisResult, error) {
	r.mu.Lock()
	r.texts = append(r.texts, request.Text)
	r.mu.Unlock()
	return SpeechSynthesisResult{
		SpeakerID: "speaker", MimeType: "audio/mpeg", Format: "mp3",
		DataURL: "data:audio/mpeg;base64," + request.Text,
	}, nil
}
