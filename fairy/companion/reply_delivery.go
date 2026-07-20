package companion

import (
	"context"
	"errors"
	"sync"
	"time"
)

type replyDeliveryRecord struct {
	Status               string
	Kind                 string
	ChainIndex           int
	PlayIndex            int
	TargetInterval       time.Duration
	PaceWait             time.Duration
	PublishedPrefixCount int
}

type replyDelivery struct {
	mu        sync.Mutex
	ctx       context.Context
	planned   int
	pacer     replyPacer
	published []ReplyChain
	err       error
	publish   func(BeatReadyCompletion) error
	record    func(replyDeliveryRecord)
}

func newReplyDelivery(ctx context.Context, planned int, publish func(BeatReadyCompletion) error, record func(replyDeliveryRecord)) *replyDelivery {
	return &replyDelivery{
		ctx:       ctx,
		planned:   planned,
		published: make([]ReplyChain, 0, planned),
		publish:   publish,
		record:    record,
	}
}

func (d *replyDelivery) Deliver(chain ReplyChain, completion BeatReadyCompletion) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}

	target := d.pacer.Target(chain.Text)
	d.emitRecord(replyDeliveryRecord{
		Status:               "planned",
		Kind:                 beatKindFinal,
		ChainIndex:           completion.ChainIndex,
		PlayIndex:            int(completion.Index),
		TargetInterval:       target,
		PublishedPrefixCount: len(d.published),
	})
	waited, err := d.pacer.wait(d.ctx, target)
	if err != nil {
		d.err = mapDeliveryError(err)
		d.emitRecord(replyDeliveryRecord{
			Status:               "cancelled",
			Kind:                 beatKindFinal,
			ChainIndex:           completion.ChainIndex,
			PlayIndex:            int(completion.Index),
			TargetInterval:       target,
			PaceWait:             waited,
			PublishedPrefixCount: len(d.published),
		})
		return d.err
	}
	if err := d.ctx.Err(); err != nil {
		d.err = mapDeliveryError(err)
		d.emitRecord(replyDeliveryRecord{
			Status:               "cancelled",
			Kind:                 beatKindFinal,
			ChainIndex:           completion.ChainIndex,
			PlayIndex:            int(completion.Index),
			TargetInterval:       target,
			PaceWait:             waited,
			PublishedPrefixCount: len(d.published),
		})
		return d.err
	}

	completion.TargetIntervalMS = target.Milliseconds()
	completion.PaceWaitMS = waited.Milliseconds()
	completion.PublishedPrefixCount = len(d.published) + 1
	if err := d.publish(completion); err != nil {
		d.err = err
		return err
	}
	d.published = append(d.published, chain)
	d.pacer.Published(chain.Text)
	d.emitRecord(replyDeliveryRecord{
		Status:               "published",
		Kind:                 beatKindFinal,
		ChainIndex:           completion.ChainIndex,
		PlayIndex:            int(completion.Index),
		TargetInterval:       target,
		PaceWait:             waited,
		PublishedPrefixCount: len(d.published),
	})
	return nil
}

func (d *replyDelivery) Cancel(chainIndex, playIndex int, current string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	target := d.pacer.Target(current)
	d.emitRecord(replyDeliveryRecord{
		Status:               "planned",
		Kind:                 beatKindFinal,
		ChainIndex:           chainIndex,
		PlayIndex:            playIndex,
		TargetInterval:       target,
		PublishedPrefixCount: len(d.published),
	})
	d.emitRecord(replyDeliveryRecord{
		Status:               "cancelled",
		Kind:                 beatKindFinal,
		ChainIndex:           chainIndex,
		PlayIndex:            playIndex,
		TargetInterval:       target,
		PublishedPrefixCount: len(d.published),
	})
	if d.err == nil {
		d.err = ErrTurnInterrupted
	}
}

func (d *replyDelivery) Snapshot() []ReplyChain {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]ReplyChain(nil), d.published...)
}

func (d *replyDelivery) Complete() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err == nil && len(d.published) == d.planned
}

func (d *replyDelivery) PlannedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.planned
}

func (d *replyDelivery) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func (d *replyDelivery) emitRecord(record replyDeliveryRecord) {
	if d.record != nil {
		d.record(record)
	}
}

func mapDeliveryError(err error) error {
	if errors.Is(err, context.Canceled) {
		return ErrTurnInterrupted
	}
	return err
}
