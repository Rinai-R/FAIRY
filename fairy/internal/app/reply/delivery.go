package reply

import (
	"context"
	"errors"
	"sync"
	"time"
)

type DeliveryRecord struct {
	Status               string
	Kind                 string
	ChainIndex           int
	PlayIndex            int
	TargetInterval       time.Duration
	PaceWait             time.Duration
	PublishedPrefixCount int
}

type Delivery struct {
	mu        sync.Mutex
	ctx       context.Context
	planned   int
	pacer     Pacer
	published []ReplyChain
	err       error
	publish   func(BeatReadyCompletion) error
	record    func(DeliveryRecord)
}

func NewDelivery(ctx context.Context, planned int, publish func(BeatReadyCompletion) error, record func(DeliveryRecord)) *Delivery {
	return &Delivery{
		ctx:       ctx,
		planned:   planned,
		published: make([]ReplyChain, 0, planned),
		publish:   publish,
		record:    record,
	}
}

func (d *Delivery) Deliver(chain ReplyChain, completion BeatReadyCompletion) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}

	target := d.pacer.Target(chain.Text)
	d.emitRecord(DeliveryRecord{
		Status:               "planned",
		Kind:                 BeatKindFinal,
		ChainIndex:           completion.ChainIndex,
		PlayIndex:            int(completion.Index),
		TargetInterval:       target,
		PublishedPrefixCount: len(d.published),
	})
	waited, err := d.pacer.WaitTarget(d.ctx, target)
	if err != nil {
		d.err = mapDeliveryError(err)
		d.emitRecord(DeliveryRecord{
			Status:               "cancelled",
			Kind:                 BeatKindFinal,
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
		d.emitRecord(DeliveryRecord{
			Status:               "cancelled",
			Kind:                 BeatKindFinal,
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
	d.emitRecord(DeliveryRecord{
		Status:               "published",
		Kind:                 BeatKindFinal,
		ChainIndex:           completion.ChainIndex,
		PlayIndex:            int(completion.Index),
		TargetInterval:       target,
		PaceWait:             waited,
		PublishedPrefixCount: len(d.published),
	})
	return nil
}

func (d *Delivery) Cancel(chainIndex, playIndex int, current string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	target := d.pacer.Target(current)
	d.emitRecord(DeliveryRecord{
		Status:               "planned",
		Kind:                 BeatKindFinal,
		ChainIndex:           chainIndex,
		PlayIndex:            playIndex,
		TargetInterval:       target,
		PublishedPrefixCount: len(d.published),
	})
	d.emitRecord(DeliveryRecord{
		Status:               "cancelled",
		Kind:                 BeatKindFinal,
		ChainIndex:           chainIndex,
		PlayIndex:            playIndex,
		TargetInterval:       target,
		PublishedPrefixCount: len(d.published),
	})
	if d.err == nil {
		d.err = ErrInterrupted
	}
}

func (d *Delivery) Snapshot() []ReplyChain {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]ReplyChain(nil), d.published...)
}

func (d *Delivery) Complete() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err == nil && len(d.published) == d.planned
}

func (d *Delivery) PlannedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.planned
}

func (d *Delivery) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func (d *Delivery) emitRecord(record DeliveryRecord) {
	if d.record != nil {
		d.record(record)
	}
}

func mapDeliveryError(err error) error {
	if errors.Is(err, context.Canceled) {
		return ErrInterrupted
	}
	return err
}
