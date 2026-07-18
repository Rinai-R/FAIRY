package companion

import (
	"context"
	"fmt"
	"strings"

	"fairy/character"
	"fairy/model"

	"go.uber.org/zap"
)

func (s *CompanionService) fillSpeechForTTS(
	ctx context.Context,
	lg *zap.Logger,
	reply CompiledReply,
	record character.Record,
	speechEnabled bool,
	conversationID string,
	connectionModel string,
) (CompiledReply, string, error) {
	if lg == nil {
		lg = zap.NewNop()
	}
	if !speechEnabled {
		return reply, "speech_disabled", nil
	}
	textLang := character.DefaultTextLanguage
	if record.TextLanguage != "" {
		textLang = record.TextLanguage
	}
	speakLang := character.DefaultSpeakingLanguage
	if record.SpeakingLanguage != "" {
		speakLang = record.SpeakingLanguage
	}
	if textLang == speakLang {
		filled, err := fillSameLanguageSpeech(reply)
		if err != nil {
			return reply, "same_language_fill_failed", err
		}
		if strings.TrimSpace(filled.SpeechText) == "" {
			return reply, "same_language_empty", nil
		}
		return filled, "", nil
	}
	translatedChains := make([]ReplyChain, len(reply.Chains))
	copy(translatedChains, reply.Chains)
	for index := range translatedChains {
		source := translatedChains[index].Text
		translated, err := s.translateDisplayText(ctx, record, source, textLang, speakLang, conversationID, connectionModel)
		if err != nil {
			lg.Warn("cognition loop",
				zap.String("phase", "speech_translate_raw"),
				zap.String("from", textLang),
				zap.String("to", speakLang),
				zap.Int("chain", index),
				zap.String("displayText", source),
				zap.String("speechText", ""),
				zap.Error(err),
			)
			return reply, "translate_failed", err
		}
		lg.Info("cognition loop",
			zap.String("phase", "speech_translate_raw"),
			zap.String("from", textLang),
			zap.String("to", speakLang),
			zap.Int("chain", index),
			zap.String("displayText", source),
			zap.String("speechText", translated),
		)
		speech := sanitizeSpeechText(translated)
		if speech == "" || validateSpeech(speech) != nil {
			return reply, "translate_unusable", fmt.Errorf("translated speech text is unusable for chain %d", index)
		}
		translatedChains[index].SpeechText = speech
	}
	filled, err := compiledReplyFromChains(translatedChains)
	if err != nil {
		return reply, "translate_unusable", err
	}
	return filled, "", nil
}

// resolveUtteranceSpeech turns a mid-ReAct display line into speakable text.
// It runs on the TTS pipeline worker, so the translate model call here never
// blocks the ReAct loop. Returning ("", nil) means "skip this utterance's audio
// silently" — a translate/validation miss must not fail the turn.
func (s *CompanionService) resolveUtteranceSpeech(
	ctx context.Context,
	lg *zap.Logger,
	record character.Record,
	line string,
	conversationID string,
	connectionModel string,
) (string, error) {
	if lg == nil {
		lg = zap.NewNop()
	}
	textLang := character.DefaultTextLanguage
	if record.TextLanguage != "" {
		textLang = record.TextLanguage
	}
	speakLang := character.DefaultSpeakingLanguage
	if record.SpeakingLanguage != "" {
		speakLang = record.SpeakingLanguage
	}
	source := line
	if textLang != speakLang {
		translated, err := s.translateDisplayText(ctx, record, line, textLang, speakLang, conversationID, connectionModel)
		if err != nil {
			lg.Warn("cognition loop",
				zap.String("phase", "utterance_translate_skip"),
				zap.String("from", textLang),
				zap.String("to", speakLang),
				zap.Error(err),
			)
			return "", nil
		}
		source = translated
	}
	speech := sanitizeSpeechText(source)
	if speech == "" || validateSpeech(speech) != nil {
		lg.Info("cognition loop", zap.String("phase", "utterance_speech_unusable"))
		return "", nil
	}
	return speech, nil
}

func (s *CompanionService) translateDisplayText(
	ctx context.Context,
	record character.Record,
	displayText string,
	textLang string,
	speakLang string,
	conversationID string,
	connectionModel string,
) (string, error) {
	input, err := BuildTranslateInput(record, displayText, textLang, speakLang)
	if err != nil {
		return "", err
	}
	request := model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane:            model.PromptLaneTranslate,
			Model:           connectionModel,
			Instructions:    TranslateInstructions,
			MaxOutputTokens: TranslateMaxOutputTokens,
			PromptCacheKey:  model.LaneCacheKey(conversationID, model.PromptLaneTranslate),
		},
		Input: input,
	}
	events, err := s.modelService.ExecuteRequestContext(ctx, request)
	if err != nil {
		return "", err
	}
	draft := strings.TrimSpace(collectText(events))
	if draft == "" {
		return "", fmt.Errorf("translate model returned empty text (%s)", summarizeStreamEvents(events))
	}
	return draft, nil
}

// BuildTranslateInput builds a stable character speech prefix plus a clear translation task.
func BuildTranslateInput(record character.Record, displayText string, textLang string, speakLang string) ([]model.PromptItem, error) {
	displayText = strings.TrimSpace(displayText)
	if displayText == "" {
		return nil, fmt.Errorf("display text is required for translation")
	}
	record.TextLanguage = textLang
	record.SpeakingLanguage = speakLang
	characterItem, err := encodeCharacterContext(record)
	if err != nil {
		return nil, err
	}
	task := fmt.Sprintf(
		"Source language: %s (%s)\nTarget speaking language: %s (%s)\nTranslate the following display line into natural spoken %s for this character's voice. Preserve meaning; apply dialogue style and mannerisms; output only the spoken line.\n\n%s",
		languageLabel(textLang),
		textLang,
		languageLabel(speakLang),
		speakLang,
		languageLabel(speakLang),
		displayText,
	)
	return []model.PromptItem{
		characterItem,
		{Type: model.PromptItemUserMessage, Content: task},
	}, nil
}

func languageLabel(code string) string {
	switch code {
	case "ja":
		return "Japanese"
	case "zh":
		return "Chinese"
	case "en":
		return "English"
	default:
		return code
	}
}

func summarizeStreamEvents(events []model.StreamEvent) string {
	if len(events) == 0 {
		return "no events"
	}
	counts := map[string]int{}
	order := make([]string, 0, len(events))
	for _, event := range events {
		if _, ok := counts[event.Type]; !ok {
			order = append(order, event.Type)
		}
		counts[event.Type]++
	}
	parts := make([]string, 0, len(order))
	for _, eventType := range order {
		parts = append(parts, fmt.Sprintf("%s=%d", eventType, counts[eventType]))
	}
	return strings.Join(parts, ",")
}
