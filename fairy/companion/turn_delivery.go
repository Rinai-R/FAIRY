package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	replyapp "fairy/reply"

	"go.uber.org/zap"
)

func (x *turnExecution) declareRespondingNode(ctx context.Context, gathered *turnGraphState, turnStarted time.Time, logger *zap.Logger) (TurnOutcome, error) {
	return x.engine.declareOutcomeNode(ctx, gathered, x.request.ConversationID, x.persisted.ID, "responding", "deliver_reply", TurnStateResponding, func() (TurnOutcome, error) {
		s := x.service
		request := x.request
		reply := gathered.reply
		connectionConfig := gathered.connectionConfig
		characterRecord := gathered.character
		if err := x.transition(TurnStateResponding); err != nil {
			return x.fail("INVALID_STATE_TRANSITION", err)
		}
		s.markTurnDelivering(request.ConversationID, x.persisted.ID)
		var profileRevision *uint64
		if gathered.profile != nil {
			value := gathered.profile.Revision
			profileRevision = &value
		}
		var firstBeatOnce sync.Once
		x.finalDelivery = replyapp.NewDelivery(
			ctx,
			len(reply.Chains),
			func(completion BeatReadyCompletion) error {
				_, err := s.publishLife(x.life, func() (TurnEvent, error) {
					return x.life.BeatReady(completion)
				})
				if err == nil && completion.Kind == replyapp.BeatKindFinal {
					firstBeatOnce.Do(func() { s.loopMetrics.firstBeat(time.Since(turnStarted)) })
				}
				return err
			},
			func(record replyapp.DeliveryRecord) {
				s.appendRuntimeLedger(
					request.ConversationID,
					x.persisted.ID,
					runtimeLedgerEventBeatDelivery,
					TurnStateResponding,
					"",
					runtimeBeatDeliveryLedgerMetadata(
						record.Status,
						record.Kind,
						record.ChainIndex,
						record.PlayIndex,
						record.TargetInterval.Milliseconds(),
						record.PaceWait.Milliseconds(),
						record.PublishedPrefixCount,
					),
				)
			},
		)
		filledChains := make([]ReplyChain, 0, len(reply.Chains))
		for index, chain := range reply.Chains {
			partial := CompiledReply{
				DisplayText: chain.Text,
				VisualState: chain.VisualState,
				Chains:      []ReplyChain{chain},
			}
			filled, skipReason, fillErr := s.fillSpeechForTTS(ctx, logger, partial, characterRecord, request.SpeechEnabled, request.ConversationID, connectionConfig.Model)
			if fillErr != nil {
				logger.Warn("cognition loop", zap.String("phase", "speech_translate_skip"), zap.String("reason", skipReason), zap.Int("chain", index), zap.Error(fillErr))
				s.appendRuntimeLedger(request.ConversationID, x.persisted.ID, runtimeLedgerEventSpeech, TurnStateResponding, "TTS_SKIPPED", map[string]any{"status": "skipped", "reason": skipReason, "index": index})
				filledChains = append(filledChains, chain)
			} else if skipReason != "" {
				logger.Info("cognition loop", zap.String("phase", "speech_translate_skip"), zap.String("reason", skipReason), zap.Int("chain", index))
				filledChains = append(filledChains, chain)
			} else {
				chain = filled.Chains[0]
				filledChains = append(filledChains, chain)
			}
			delta := chain.Text
			if index > 0 {
				delta = "\n" + chain.Text
			}
			if _, err := s.publishLife(x.life, func() (TurnEvent, error) {
				return x.life.ReplyChain(uint8(index), delta, chain)
			}); err != nil {
				return x.fail("MODEL_RESPONSE_INVALID", err)
			}
			speechText := strings.TrimSpace(chain.SpeechText)
			if speechText != "" && speechExceedsSoftLimit(speechText) {
				logger.Warn("cognition loop",
					zap.String("phase", "speech_over_soft_limit"),
					zap.Int("chain", index),
					zap.Int("runes", utf8.RuneCountInString(speechText)),
					zap.Int("softLimit", replyapp.MaxSpeechChars),
				)
			}
			if x.speechFlow == nil {
				if request.SpeechEnabled {
					s.appendRuntimeLedger(request.ConversationID, x.persisted.ID, runtimeLedgerEventSpeech, TurnStateResponding, "TTS_SKIPPED", map[string]any{"status": "skipped", "reason": "speech_synthesizer_unavailable", "index": index})
				}
				play := x.speechPlayIndex
				x.speechPlayIndex++
				if err := x.finalDelivery.Deliver(chain, BeatReadyCompletion{
					BeatID:      fmt.Sprintf("final-%d", index),
					Kind:        replyapp.BeatKindFinal,
					Index:       uint8(play),
					ChainIndex:  index,
					DisplayText: chain.Text,
					SpeechText:  speechText,
					VisualState: chain.VisualState,
				}); err != nil {
					code := "MODEL_RESPONSE_INVALID"
					if errors.Is(err, ErrTurnInterrupted) {
						code = "TURN_INTERRUPTED"
					}
					return x.fail(code, err)
				}
				continue
			}
			if speechText != "" && !x.speechRequested {
				if _, err := s.publishLife(x.life, func() (TurnEvent, error) {
					return x.life.SpeechRequested(TurnCompletion{
						Text:                chain.Text,
						SpeechText:          speechText,
						CharacterRevision:   characterRecord.Revision,
						UserProfileRevision: profileRevision,
					})
				}); err != nil {
					logger.Warn("tts request skipped", zap.String("turn", x.persisted.ID), zap.Error(err))
				} else {
					x.speechRequested = true
				}
			}
			play := x.speechPlayIndex
			x.speechPlayIndex++
			chainSpeech := speechText
			beatID := fmt.Sprintf("final-%d", index)
			s.appendRuntimeLedger(request.ConversationID, x.persisted.ID, runtimeLedgerEventSpeech, TurnStateResponding, "", map[string]any{
				"status":         "queued",
				"index":          index,
				"beatId":         beatID,
				"playIndex":      play,
				"speechTextHash": runtimeHash(chainSpeech),
			})
			chainDisplay := chain.Text
			chainVisual := chain.VisualState
			x.speechFlow.Enqueue(replyapp.SpeechJob{
				BeatID:      beatID,
				Kind:        replyapp.BeatKindFinal,
				PlayIndex:   play,
				ChainIndex:  index,
				DisplayText: chainDisplay,
				VisualState: chainVisual,
				Resolve:     func() (string, error) { return chainSpeech, nil },
			})
		}
		rebuilt, rebuildErr := compiledReplyFromChains(filledChains)
		if rebuildErr != nil {
			return x.fail("MODEL_RESPONSE_INVALID", rebuildErr)
		}
		reply = rebuilt
		if x.speechFlow != nil {
			x.speechFlow.Close()
		}
		if err := x.finalDelivery.Err(); err != nil {
			code := "MODEL_RESPONSE_INVALID"
			if errors.Is(err, ErrTurnInterrupted) {
				code = "TURN_INTERRUPTED"
			}
			return x.fail(code, err)
		}
		if !x.finalDelivery.Complete() {
			return x.fail("MODEL_RESPONSE_INVALID", errors.New("final reply delivery did not publish every planned chain"))
		}
		gathered.reply = reply
		gathered.profileRevision = profileRevision
		return TurnOutcome{}, nil
	})
}
