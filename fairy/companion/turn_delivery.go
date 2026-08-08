package companion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	replyapp "fairy/reply"
	"fairy/session"

	"go.uber.org/zap"
)

func (x *turnExecution) deliverReply(ctx context.Context, gathered *turnContext, turnStarted time.Time, logger *zap.Logger) (outcome TurnOutcome, err error) {
	s := x.service
	request := x.request
	compiled := gathered.reply
	if err := x.transition(turnStateResponding); err != nil {
		return x.fail("INVALID_STATE_TRANSITION", err)
	}
	deliverySpan := s.startMessageSpan(request.TraceID, "回复交付", "delivery", map[string]string{
		"chainCount": fmt.Sprint(len(compiled.Chains)),
	})
	defer func() {
		status := "completed"
		attributes := map[string]string{"chainCount": fmt.Sprint(len(compiled.Chains))}
		if err != nil {
			status = "failed"
			attributes["errorCode"] = "SURFACE_DELIVERY_FAILED"
		}
		s.finishMessageSpan(deliverySpan, status, attributes)
	}()
	s.markTurnDelivering(request.ConversationID, x.persisted.ID)
	var profileRevision *uint64
	if gathered.profile != nil {
		value := gathered.profile.Revision
		profileRevision = &value
	}
	var firstBeatOnce sync.Once
	x.finalDelivery = replyapp.NewDelivery(
		ctx,
		len(compiled.Chains),
		func(completion BeatReadyCompletion) error {
			publish := func() error {
				_, err := s.publishLife(x.life, func() (session.Event, error) {
					return x.life.BeatReady(completion)
				})
				return err
			}
			var err error
			if completion.Chain != nil && completion.Chain.Kind == replyapp.ChainSticker {
				err = s.expressionDeliveries.await(ctx, expressionDeliveryKey{
					conversationID: request.ConversationID,
					turnID:         x.persisted.ID,
					beatID:         completion.BeatID,
				}, publish)
			} else {
				err = publish()
			}
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
				turnStateResponding,
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
	for index, chain := range compiled.Chains {
		delta := chain.Text
		if chain.Kind != replyapp.ChainSticker && index > 0 {
			delta = "\n" + chain.Text
		}
		if _, err := s.publishLife(x.life, func() (session.Event, error) {
			return x.life.ReplyChain(uint8(index), delta, chain)
		}); err != nil {
			return x.fail("MODEL_RESPONSE_INVALID", err)
		}
		beat := BeatReadyCompletion{
			BeatID:      fmt.Sprintf("final-%d", index),
			Kind:        replyapp.BeatKindFinal,
			Index:       uint8(x.beatIndex),
			ChainIndex:  index,
			DisplayText: chain.Text,
			VisualState: chain.VisualState,
		}
		x.beatIndex++
		if err := x.finalDelivery.Deliver(chain, beat); err != nil {
			code := "SURFACE_DELIVERY_FAILED"
			if errors.Is(err, ErrTurnInterrupted) {
				code = "TURN_INTERRUPTED"
			}
			return x.fail(code, err)
		}
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
	gathered.profileRevision = profileRevision
	logger.Debug("reply delivered", zap.Int("chains", len(compiled.Chains)))
	return TurnOutcome{}, nil
}
