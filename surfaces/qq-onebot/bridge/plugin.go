package bridge

import (
	"context"
	"errors"
	"strconv"

	zero "github.com/wdvxdr1123/ZeroBot"
)

type Plugin struct {
	ctx       context.Context
	selfID    int64
	allowlist map[int64]struct{}
	lanes     *Lanes
	dedupe    *Dedupe
	runner    *Runner
	actions   func(*zero.Ctx) Actions
	report    func(string, error)
}

type Actions interface {
	ActionSender
	ReplyVerifier
}

func NewPlugin(ctx context.Context, cfg Config, runner *Runner) (*Plugin, error) {
	if ctx == nil || runner == nil {
		return nil, errors.New("plugin context and runner are required")
	}
	selfID, err := strconv.ParseInt(cfg.SelfID, 10, 64)
	if err != nil || selfID <= 0 {
		return nil, errors.New("OneBot self ID must be a positive integer")
	}
	allowlist := make(map[int64]struct{}, len(cfg.GroupAllowlist))
	groups := make([]string, 0, len(cfg.GroupAllowlist))
	for _, raw := range cfg.GroupAllowlist {
		groupID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || groupID <= 0 {
			return nil, errors.New("OneBot group allowlist entries must be positive integers")
		}
		allowlist[groupID] = struct{}{}
		groups = append(groups, raw)
	}
	return &Plugin{
		ctx:       ctx,
		selfID:    selfID,
		allowlist: allowlist,
		lanes:     NewLanes(ctx, groups, 16),
		dedupe:    NewDedupe(2048),
		runner:    runner,
		actions:   func(ctx *zero.Ctx) Actions { return ZeroActions{Context: ctx} },
	}, nil
}

func (p *Plugin) Register(engine *zero.Engine) {
	engine.OnMessage(zero.OnlyGroup).Handle(p.Handle)
}

func (p *Plugin) Handle(zeroCtx *zero.Ctx) {
	if p == nil || zeroCtx == nil || p.ctx.Err() != nil {
		return
	}
	event, ok := groupEventFromZero(zeroCtx.Event)
	if !ok || !p.dedupe.Add(event.ID) {
		return
	}
	actions := p.actions(zeroCtx)
	input, triggered, err := TriggerInput(p.ctx, event, p.selfID, p.allowlist, actions)
	if err != nil {
		p.reportError("trigger", err)
		return
	}
	if !triggered {
		return
	}
	groupKey := strconv.FormatInt(event.GroupID, 10)
	if err := p.lanes.Submit(groupKey, func(jobCtx context.Context) {
		if err := p.runner.Handle(jobCtx, event.GroupID, input, actions); err != nil {
			p.reportError("turn", err)
		}
	}); err != nil {
		p.reportError("queue", err)
	}
}

func (p *Plugin) Close() {
	if p != nil && p.lanes != nil {
		p.lanes.Close()
	}
}

func (p *Plugin) reportError(stage string, err error) {
	if p != nil && p.report != nil && err != nil {
		p.report(stage, err)
	}
}
