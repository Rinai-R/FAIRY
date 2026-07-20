package companion

import (
	"context"
	"strings"
	"time"
	"unicode"
)

const (
	replyPaceBase        = 220 * time.Millisecond
	replyPaceCJKRune     = 70 * time.Millisecond
	replyPaceOtherRune   = 35 * time.Millisecond
	replyPaceStrongPause = 220 * time.Millisecond
	replyPaceWeakPause   = 100 * time.Millisecond
	replyPaceMinimum     = 420 * time.Millisecond
	replyPaceMaximum     = 2200 * time.Millisecond
)

func targetReplyInterval(previous, current string) time.Duration {
	interval := replyPaceBase
	for _, r := range current {
		if unicode.IsSpace(r) {
			continue
		}
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			interval += replyPaceCJKRune
			continue
		}
		interval += replyPaceOtherRune
	}
	switch lastNonSpaceRune(previous) {
	case '。', '！', '？', '!', '?':
		interval += replyPaceStrongPause
	case '，', '、', '；', ';':
		interval += replyPaceWeakPause
	}
	return min(max(interval, replyPaceMinimum), replyPaceMaximum)
}

func lastNonSpaceRune(value string) rune {
	runes := []rune(strings.TrimRightFunc(value, unicode.IsSpace))
	if len(runes) == 0 {
		return 0
	}
	return runes[len(runes)-1]
}

type replyPacer struct {
	lastPublished time.Time
	previousText  string
}

func (p *replyPacer) Wait(ctx context.Context, current string) (target time.Duration, waited time.Duration, err error) {
	target = p.Target(current)
	if target == 0 {
		return 0, 0, nil
	}
	waited, err = p.wait(ctx, target)
	return target, waited, err
}

func (p *replyPacer) Target(current string) time.Duration {
	if p.lastPublished.IsZero() {
		return 0
	}
	return targetReplyInterval(p.previousText, current)
}

func (p *replyPacer) wait(ctx context.Context, target time.Duration) (time.Duration, error) {
	remaining := time.Until(p.lastPublished.Add(target))
	if remaining <= 0 {
		return 0, nil
	}
	started := time.Now()
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return time.Since(started), ctx.Err()
	case <-timer.C:
		return time.Since(started), nil
	}
}

func (p *replyPacer) Published(text string) {
	p.previousText = text
	p.lastPublished = time.Now()
}
