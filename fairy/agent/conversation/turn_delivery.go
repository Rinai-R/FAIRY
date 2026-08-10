package conversation

import (
	"context"
	"errors"
	"fairy/agent/conversation/delivery"
	"fairy/agent/conversation/lifecycle"
	"fairy/agent/reply"
	"fairy/transport/session"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

func (x *turnExecution) deliverReply(ctx context.Context, gathered *turnContext, turnStarted time.Time, logger *zap.Logger) (outcome TurnOutcome, err error) {
	s := x.service
	request := x.request
	compiled := gathered.reply
	if err := x.transition(lifecycle.StateResponding); err != nil {
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
	var profileRevision *uint64
	if gathered.profile != nil {
		value := gathered.profile.Revision
		profileRevision = &value
	}
	var firstBeatOnce sync.Once
	x.finalDelivery = reply.NewDelivery(
		ctx,
		len(compiled.Chains),
		func(completion reply.BeatReadyCompletion) error {
			completion.ReplyTargetMessageID = request.ReplyTargetMessageID
			publish := func() error {
				_, err := s.publishLife(x.life, func() (session.Event, error) {
					return x.life.BeatReady(completion)
				})
				return err
			}
			key := delivery.Key{
				ConversationID: request.ConversationID,
				TurnID:         x.persisted.ID,
				BeatID:         completion.BeatID,
			}
			delivered, err := s.expressionDeliveries.AwaitResult(ctx, key, publish)
			status, externalMessageID, errorCode := surfaceDeliveryTelemetry(delivered, err)
			s.recordSurfaceDelivery(key.TurnID, key.BeatID, status, externalMessageID, errorCode)
			if err == nil && completion.Kind == reply.BeatKindFinal {
				firstBeatOnce.Do(func() { s.loopMetrics.firstBeat(time.Since(turnStarted)) })
			}
			return err
		},
		func(record reply.DeliveryRecord) {
			s.appendRuntimeLedger(
				request.ConversationID,
				x.persisted.ID,
				runtimeLedgerEventBeatDelivery,
				lifecycle.StateResponding,
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
		if chain.Kind != reply.ChainSticker && index > 0 {
			delta = "\n" + chain.Text
		}
		if _, err := s.publishLife(x.life, func() (session.Event, error) {
			return x.life.ReplyChain(uint8(index), delta, chain)
		}); err != nil {
			return x.fail("MODEL_RESPONSE_INVALID", err)
		}
		beat := reply.BeatReadyCompletion{
			BeatID:      fmt.Sprintf("final-%d", index),
			Kind:        reply.BeatKindFinal,
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

func surfaceDeliveryTelemetry(result session.ExpressionDeliveryResult, err error) (status, externalMessageID, errorCode string) {
	if err == nil {
		return string(session.ExpressionDeliverySucceeded), result.ExternalMessageID, ""
	}
	if result.Status == session.ExpressionDeliveryFailed {
		return string(session.ExpressionDeliveryFailed), "", "SURFACE_DELIVERY_FAILED"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrTurnInterrupted) {
		return "interrupted", "", "SURFACE_DELIVERY_INTERRUPTED"
	}
	if strings.Contains(err.Error(), "timed out") {
		return string(session.ExpressionDeliveryFailed), "", "SURFACE_DELIVERY_TIMEOUT"
	}
	return string(session.ExpressionDeliveryFailed), "", "SURFACE_DELIVERY_UNAVAILABLE"
}
