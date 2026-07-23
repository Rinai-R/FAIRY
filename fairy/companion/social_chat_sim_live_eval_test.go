//go:build live

package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/config"
	"fairy/memory"
	"fairy/model"
)

func TestLiveSimulateSREGroupChat(t *testing.T) {
	runLiveGroupChatSimulation(t, liveGroupChatScenario{
		name: "sre",
		all:  sreGroupChatObservationsFull(),
		seed: []memory.SocialMemoryEntry{
			{ID: "ep-sre", Kind: memory.SocialMemoryEpisode, Situation: "群友聊SRE和运维边界", Content: "大家会争论挂名SRE实际在干运维，以及自动化检测恢复才算真SRE", RecallCue: "SRE运维infra"},
			{ID: "bh-chat", Kind: memory.SocialMemoryBehavior, Situation: "职场吐槽串台时", Content: "不抢话不说教，轻接一句共鸣即可", RecallCue: "职场吐槽"},
			{ID: "ex-tired", Kind: memory.SocialMemoryExpression, Situation: "群友说上班累想读书", Content: "短句共情，不劝退也不硬鸡汤", RecallCue: "上班累读书"},
		},
		newTail: 4,
	})
}

func TestLiveSimulateGalgameDualPlayChat(t *testing.T) {
	runLiveGroupChatSimulation(t, liveGroupChatScenario{
		name: "galgame-dual-play",
		all:  galgameDualPlayObservations(),
		seed: []memory.SocialMemoryEntry{
			{ID: "ep-gal", Kind: memory.SocialMemoryEpisode, Situation: "群友聊同人展和gal双开", Content: "有人吐槽邮展人少、西安还没办过galo；后面转到罚抄和向日葵教会双开吃不消", RecallCue: "同人展 gal 双开 罚抄 向日葵"},
			{ID: "bh-dual", Kind: memory.SocialMemoryBehavior, Situation: "群友纠结要不要双开剧本时", Content: "先接住纠结，不替对方做决定，可顺着停一条线的方向轻轻附和", RecallCue: "双开停线"},
			{ID: "ex-gal", Kind: memory.SocialMemoryExpression, Situation: "吐槽十天进度难绷", Content: "短句接梗，别说教别列清单", RecallCue: "进度难绷"},
		},
		newTail: 5,
	})
}

func TestLiveSimulateGalgameAmbientInboxClient(t *testing.T) {
	persona := loadPersonaLiveConfig(t)
	modelPort := newLiveModelPort(t, persona)
	all := galgameDualPlayObservations()

	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "ep-gal", Kind: memory.SocialMemoryEpisode, Situation: "群友聊同人展和gal双开", Content: "有人吐槽邮展人少、西安还没办过galo；后面转到罚抄和向日葵教会双开吃不消", RecallCue: "同人展 gal 双开 罚抄 向日葵"},
		{ID: "bh-dual", Kind: memory.SocialMemoryBehavior, Situation: "群友纠结要不要双开剧本时", Content: "先接住纠结，不替对方做决定，可顺着停一条线的方向轻轻附和", RecallCue: "双开停线"},
		{ID: "ex-gal", Kind: memory.SocialMemoryExpression, Situation: "吐槽十天进度难绷", Content: "短句接梗，别说教别列清单", RecallCue: "进度难绷"},
	}}}
	service := newSocialLearningTestService(memoryPort, modelPort)
	service.cfg = livePersonaConfig{model: persona.Model}
	defer service.Close()

	var (
		mu        sync.Mutex
		decisions []string
		replies   []string
	)
	service.ambient.decideHook = func(ctx context.Context, batch ambientBatch) (ParticipationResult, error) {
		started := time.Now()
		result, err := service.DecideParticipation(ctx, ParticipationRequest{
			ConversationID:   batch.conversationID,
			EvaluationReason: batch.evaluationReason,
			Messages:         batch.messages,
			CacheMessages:    batch.cacheMessages,
		})
		elapsed := time.Since(started).Milliseconds()
		if err != nil {
			mu.Lock()
			if ctx.Err() != nil {
				decisions = append(decisions, fmt.Sprintf("gen=%d reason=%s canceled (%dms)", batch.generation, batch.evaluationReason, elapsed))
			} else {
				decisions = append(decisions, fmt.Sprintf("gen=%d reason=%s failed (%dms): %v", batch.generation, batch.evaluationReason, elapsed, err))
			}
			mu.Unlock()
			return ParticipationResult{}, err
		}
		newCount := 0
		for _, message := range batch.messages {
			if message.IsNew {
				newCount++
			}
		}
		line := fmt.Sprintf("gen=%d reason=%s window=%d new=%d cache=%d action=%s (%dms)",
			batch.generation, batch.evaluationReason, len(batch.messages), newCount, len(batch.cacheMessages), result.Action, elapsed)
		if result.TargetMessageID != nil {
			line += fmt.Sprintf(" target=%s %q", *result.TargetMessageID, observationTextByID(batch.messages, *result.TargetMessageID))
		}
		if result.WaitSeconds != nil {
			line += fmt.Sprintf(" wait=%ds", *result.WaitSeconds)
		}
		mu.Lock()
		decisions = append(decisions, line)
		mu.Unlock()
		t.Log(line)
		return result, nil
	}
	service.ambient.after = func(delay time.Duration, callback func()) stoppableTimer {
		if delay > 150*time.Millisecond {
			delay = 150 * time.Millisecond
		}
		return time.AfterFunc(delay, callback)
	}
	service.ambient.submitHook = func(request SubmitTurnRequest) (TurnOutcome, error) {
		if request.ReplyIntent == nil {
			return TurnOutcome{}, errors.New("reply intent missing")
		}
		service.ambient.mu.Lock()
		state := service.ambient.states[request.ConversationID]
		cache := make([]AmbientObservation, 0)
		if state != nil {
			for _, entry := range state.cacheMessages {
				cache = append(cache, entry.observation)
			}
		}
		service.ambient.mu.Unlock()
		if len(cache) == 0 {
			cache = all
		}
		started := time.Now()
		draft, tools, _, err := livePublicRespondWithTools(context.Background(), service, modelPort, persona.Model, cache, request.ReplyIntent)
		if err != nil {
			return TurnOutcome{}, err
		}
		display := draft
		if compiled, compileErr := CompileReply(draft, []VisualState{{ID: "idle", Description: "待机"}}); compileErr == nil {
			display = compiled.DisplayText
		}
		mu.Lock()
		replies = append(replies, display)
		mu.Unlock()
		t.Logf("submit reply (%dms, tools=%v): %q", time.Since(started).Milliseconds(), tools, display)
		return TurnOutcome{ResponseText: display}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	totalStarted := time.Now()
	t.Logf("ambient inbox client sim model=%s messages=%d", persona.Model, len(all))

	for index, incoming := range all {
		if err := ctx.Err(); err != nil {
			t.Fatalf("feed aborted: %v", err)
		}
		obs := incoming
		obs.IsNew = false
		if err := service.ObserveAmbient("conversation-1", obs); err != nil {
			t.Fatalf("ObserveAmbient #%d: %v", index+1, err)
		}
		t.Logf("feed #%02d %s: %s", index+1, incoming.SenderName, truncateRunes(incoming.Text, 36))
		if index+1 >= len(all) {
			break
		}
		gap := time.Duration(all[index+1].TimestampUnixMS-incoming.TimestampUnixMS) * time.Millisecond
		if gap < 0 {
			gap = 0
		}
		// Long real-world pauses: let the current single-flight decision settle (true ambient quiet).
		if gap >= 5*time.Minute {
			t.Logf("quiet gap=%s → wait idle", gap)
			quietDeadline := time.Now().Add(2 * time.Minute)
			for time.Now().Before(quietDeadline) {
				if ambientInboxIdle(service, "conversation-1") {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			continue
		}
		// Short bursts: compress arrival spacing; do NOT wait for decide/reply.
		sleepFor := gap / 50
		if sleepFor > 300*time.Millisecond {
			sleepFor = 300 * time.Millisecond
		}
		if sleepFor < 30*time.Millisecond {
			sleepFor = 30 * time.Millisecond
		}
		time.Sleep(sleepFor)
	}

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if ambientInboxIdle(service, "conversation-1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ambientInboxIdle(service, "conversation-1") {
		t.Fatal("ambient inbox did not become idle after feed")
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("done decisions=%d replies=%d end_to_end_ms=%d", len(decisions), len(replies), time.Since(totalStarted).Milliseconds())
	for i, decision := range decisions {
		t.Logf("decision[%d]=%s", i+1, decision)
	}
	for i, reply := range replies {
		t.Logf("bot[%d]=%q", i+1, reply)
	}
}

func ambientInboxIdle(service *CompanionService, conversationID string) bool {
	if service == nil || service.ambient == nil {
		return true
	}
	service.ambient.mu.Lock()
	defer service.ambient.mu.Unlock()
	state := service.ambient.states[conversationID]
	if state == nil {
		return true
	}
	return !state.running && state.timer == nil
}

func truncateRunes(text string, limit int) string {
	runes := []rune(strings.ReplaceAll(text, "\n", " "))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

type liveGroupChatScenario struct {
	name    string
	all     []AmbientObservation
	seed    []memory.SocialMemoryEntry
	newTail int
}

func runLiveGroupChatSimulation(t *testing.T, scenario liveGroupChatScenario) {
	t.Helper()
	persona := loadPersonaLiveConfig(t)
	modelPort := newLiveModelPort(t, persona)

	all := append([]AmbientObservation(nil), scenario.all...)
	if len(all) == 0 {
		t.Fatal("empty scenario")
	}
	if len(all) > maxAmbientCacheObservations {
		t.Fatalf("scenario has %d messages, cache max is %d", len(all), maxAmbientCacheObservations)
	}
	window := all
	if len(window) > maxAmbientObservations {
		window = window[len(window)-maxAmbientObservations:]
	}
	newTail := scenario.newTail
	if newTail < 1 {
		newTail = 3
	}
	if newTail > len(window) {
		newTail = len(window)
	}
	for i := range window {
		window[i].IsNew = i >= len(window)-newTail
	}
	t.Logf("scenario=%s total=%d window=%d newTail=%d model=%s", scenario.name, len(all), len(window), newTail, persona.Model)

	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: scenario.seed}}
	service := newSocialLearningTestService(memoryPort, modelPort)
	service.cfg = livePersonaConfig{model: persona.Model}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	participateStarted := time.Now()
	result, err := service.DecideParticipation(ctx, ParticipationRequest{
		ConversationID:   "conversation-1",
		EvaluationReason: ParticipationReasonMessage,
		Messages:         window,
		CacheMessages:    all,
	})
	participateMS := time.Since(participateStarted).Milliseconds()
	if err != nil {
		t.Fatalf("DecideParticipation: %v", err)
	}
	t.Logf("latency participate_ms=%d", participateMS)
	t.Logf("participate action=%s wait=%v", result.Action, result.WaitSeconds)
	if result.TargetMessageID != nil {
		t.Logf("target=%s text=%q", *result.TargetMessageID, observationTextByID(all, *result.TargetMessageID))
	}
	if result.Intent != nil {
		t.Logf("intent act=%q focus=%q mode=%q memoryQuery=%q expressionQuery=%q drift=%q",
			result.Intent.ReplyAct, result.Intent.Focus, result.Intent.ReplyMode,
			result.Intent.MemoryQuery, result.Intent.ExpressionQuery, result.Intent.DriftLevel)
	}
	if result.Action != ParticipationReply || result.Intent == nil {
		t.Log("simulation stopped at participate (no reply)")
		return
	}

	respondStarted := time.Now()
	reply, tools, phases, err := livePublicRespondWithTools(ctx, service, modelPort, persona.Model, all, result.Intent)
	respondMS := time.Since(respondStarted).Milliseconds()
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	t.Logf("latency respond_ms=%d end_to_end_ms=%d", respondMS, participateMS+respondMS)
	for _, phase := range phases {
		t.Logf("latency respond_phase=%s", phase)
	}
	t.Logf("respond tools=%v", tools)
	t.Logf("respond draft=%s", reply)
	if compiled, compileErr := CompileReply(reply, []VisualState{{ID: "idle", Description: "待机"}}); compileErr != nil {
		t.Logf("compile note: %v", compileErr)
	} else {
		t.Logf("respond display=%q", compiled.DisplayText)
	}
}

func observationTextByID(messages []AmbientObservation, id string) string {
	for _, message := range messages {
		if message.MessageID == id {
			return message.Text
		}
	}
	return ""
}

func sreGroupChatObservations() []AmbientObservation {
	all := sreGroupChatObservationsFull()
	if len(all) > maxAmbientObservations {
		return all[len(all)-maxAmbientObservations:]
	}
	return all
}

func sreGroupChatObservationsFull() []AmbientObservation {
	base := time.Date(2026, 7, 23, 23, 16, 57, 0, time.Local).UnixMilli()
	rows := []chatSimRow{
		{"HikariLan贺兰星辰", "可能周六", 0},
		{"楚玉衡", "[爱心]", 7_000},
		{"q1ngke", "其实你的感觉没错", 9_000},
		{"q1ngke", "SRE很多都是挂着SRE的名字干运维", 26_000},
		{"Shanwer", "但是运维又不只是sre", 34_000},
		{"q1ngke", "所以这也是为什么AI SRE现在单独分化出来的", 36_000},
		{"聊斋仙", "@HikariLan贺兰星辰 hl大人请指引我", 70_000},
		{"聊斋仙", "😭😭😭", 73_000},
		{"q1ngke", "传统运维就是系统坏了，我去修；服务要上线，我来部署。", 78_000},
		{"q1ngke", "但是其实真正的SRE应该是，为什么这个问题需要人修？能不能做成自动检测、自动恢复、自动扩容、自动回滚？", 94_000},
		{"q1ngke", "所以不可避免地就导致了要搞一堆云原生，搞一堆运维的东西", 113_000},
		{"q1ngke", "这也就是为什么SRE看起来和运维非常像", 124_000},
		{"楚玉衡", "这不就是infra开发", 132_000},
		{"楚玉衡", "宝宝", 133_000},
		{"q1ngke", "其实只要涉及到底层平台和系统能力都可以称之为Infra啊真要算起来", 157_000},
		{"Shanwer", "在外面名头都是自己给的", 176_000},
		{"q1ngke", "对，反正大家肯定喜欢吹牛逼，所以当然是把自己吹的牛一点可以在就业市场上卖个高价", 202_000},
		{"q1ngke", "其实这没啥不好的，甚至我觉得还不错，因为在吹牛的时候也会想自己哪里还不够，说不定就会去深造，搞不好真成了某个Infra方向的大手子", 253_000},
		{"q1ngke", "[图片]", 260_000},
		{"白袍", "指点我一下好吗", 267_000},
		{"HikariLan贺兰星辰", "[图片]", 273_000},
		{"HikariLan贺兰星辰", "别串", 279_000},
		{"聊斋仙", "指点我一下好吗", 286_000},
		{"白袍", "[图片]", 305_000},
		{"KyuuYukiNA", "@HikariLan贺兰星辰", 311_000},
		{"KyuuYukiNA", "贺兰教我", 317_000},
		{"HikariLan贺兰星辰", "[图片]", 317_000},
		{"Shanwer", "贺兰教我", 328_000},
		{"Shanwer", "一字一句把我拉出迷雾里", 341_000},
		{"聊斋仙", "哎，hl都不回我的，懂你意思，指点了也没什么希望，我自己走", 344_000},
		{"聊斋仙", "[图片]", 346_000},
		{"白袍", "前段时间都想辞职去读书了", 380_000},
		{"keiraee", "这群撒时候来了这么多人 我记得就几十个啊", 426_000},
		{"keiraee", "[挥手]", 429_000},
		{"KyuuYukiNA", "刚下班", 438_000},
		{"KyuuYukiNA", "累成狗了", 440_000},
		{"Yso4rie1", "@白袍 真的假的", 441_000},
		{"KyuuYukiNA", "[图片]", 442_000},
		{"白袍", "真的，后来又放弃了", 462_000},
		{"花园多惠channel", "啊这，俺倒是不想在读书了", 520_000},
		{"Shanwer", "想当无忧无虑的jk", 586_000},
		{"白袍", "[图片]", 714_000},
		{"白袍", "后来觉得直接辞太孤注一掷", 735_000},
		{"楚玉衡", "想当无忧无虑的ak", 746_000},
		{"楚玉衡", "只需要突突突", 751_000},
		{"楚玉衡", "直到枪管过热或没子弹", 758_000},
		{"轩辕韵白", "还是读书好", 806_000},
		{"KyuuYukiNA", "是的", 833_000},
		{"KyuuYukiNA", "上班好累", 835_000},
		{"KyuuYukiNA", "😭", 837_000},
		{"轩辕韵白", "我已继续读书", 876_000},
		{"轩辕韵白", "[图片]", 878_000},
		{"白袍", "我早已麻痹", 909_000},
	}
	return rowsToObservations(base, rows)
}

func galgameDualPlayObservations() []AmbientObservation {
	base := time.Date(2026, 7, 23, 20, 32, 51, 0, time.Local).UnixMilli()
	rows := []chatSimRow{
		{"灰魔女", "[图片]", 0},
		{"月影", "@藍原アリス 你邮竟然就我一个人(", 176_000},
		{"月影", "隔壁西⚡很多人", 192_000},
		{"藍原アリス", "没钱", 242_000},
		{"藍原アリス", "[图片]", 246_000},
		{"灰魔女", "人家人多（确信）", 297_000},
		{"灰魔女", "[图片]", 304_000},
		{"藍原アリス", "要是开在西安我肯定去", 348_000},
		{"德欧门•弗瑞厄尔", "战犯言论\n[图片]", 1_066_000},
		{"德欧门•弗瑞厄尔", "到时候必须关地牢里", 1_078_000},
		{"烨之", "@请去玩ISLAND 主要还要和生活对线", 2_279_000},
		{"烨之", "有时间肯定想去[流泪]", 2_291_000},
		{"月影", "没事等我到了带你云游()", 2_709_000},
		{"二阶堂真红蓝青空", "@Adobsidian25 什么情况", 6_107_000},
		{"R1ckzy", "西安还没有人整过galo了", 7_225_000},
		{"R1ckzy", "印象中", 7_242_000},
		{"neco arc", "感觉有点吃不消了", 9_267_000},
		{"neco arc", "罚抄跟向日葵教会", 9_280_000},
		{"瑞希ResciA", "向日葵教会很好玩啊", 9_293_000},
		{"neco arc", "这两个同时推有点", 9_296_000},
		{"瑞希ResciA", "[图片]", 9_296_000},
		{"瑞希ResciA", "同时？！", 9_303_000},
		{"瑞希ResciA", "双开这俩有点呃呃", 9_312_000},
		{"neco arc", "所以我在思考🤔", 9_331_000},
		{"neco arc", "要不先把罚抄停了", 9_343_000},
		{"月影", "停一个", 9_370_000},
		{"月影", "我一直不太赞成双开(", 9_379_000},
		{"月影", "废萌还好", 9_384_000},
		{"阮沧", "双开啥都玩不了啊", 9_401_000},
		{"阮沧", "除非一条线一条线", 9_408_000},
		{"月影", "你是对的，我就从来不双开", 9_423_000},
		{"阮沧", "双开的话上一条对我来说就大概率弃了", 9_436_000},
		{"瑞希ResciA", "双开一般我是另一个确实是不带脑子随便玩才开", 9_501_000},
		{"瑞希ResciA", "不然我开另一个的时候基本是不想玩上一个了", 9_513_000},
		{"neco arc", "罚抄玩到找超自然研究部", 9_517_000},
		{"neco arc", "向日葵才玩到众人齐聚一堂", 9_534_000},
		{"neco arc", "十天玩到这有点难绷", 9_551_000},
		{"瑞希ResciA", "那得看你想打什么", 9_552_000},
		{"月影", "不玩向日葵了", 9_583_000},
		{"月影", "继续罚", 9_586_000},
	}
	return rowsToObservations(base, rows)
}

type chatSimRow struct {
	name, text string
	ms         int64
}

func rowsToObservations(base int64, rows []chatSimRow) []AmbientObservation {
	out := make([]AmbientObservation, 0, len(rows))
	for i, item := range rows {
		sender := strings.TrimSpace(item.name)
		out = append(out, AmbientObservation{
			MessageID:       fmt.Sprintf("m%d", i+1),
			SenderID:        "u-" + sender,
			SenderName:      sender,
			Text:            item.text,
			TimestampUnixMS: base + item.ms,
		})
	}
	return out
}

func livePublicRespondWithTools(
	ctx context.Context,
	service *CompanionService,
	modelPort ModelPort,
	modelName string,
	messages []AmbientObservation,
	intent *ReplyIntent,
) (string, []string, []string, error) {
	resolved := publicAmbientResolved()
	toolsUsed := make([]string, 0, 3)
	phases := make([]string, 0, 6)
	retrieval := memory.RetrievalContext{}
	socialStarted := time.Now()
	social, err := service.retrieveSocialRespondContext(ctx, "character-1", "conversation-1", resolved, intent, ambientSenderIDs(messages))
	if err != nil {
		return "", nil, nil, err
	}
	phases = append(phases, fmt.Sprintf("preinject_ms=%d", time.Since(socialStarted).Milliseconds()))
	dialogueMessages := make([]map[string]string, 0, len(messages))
	for _, message := range messages {
		dialogueMessages = append(dialogueMessages, map[string]string{
			"role": "user", "sender": message.SenderName, "text": message.Text,
		})
	}
	dialoguePayload, err := json.Marshal(map[string]any{"contextType": "dialogue", "messages": dialogueMessages})
	if err != nil {
		return "", nil, nil, err
	}
	intentPayload, err := json.Marshal(map[string]any{
		"contextType": "public_reply_intent", "replyAct": intent.ReplyAct, "tone": intent.Tone,
		"relationshipSignal": intent.RelationshipSignal, "replyMode": intent.ReplyMode,
		"focus": intent.Focus, "avoid": intent.Avoid, "referenceInfo": intent.ReferenceInfo,
		"memoryQuery": intent.MemoryQuery, "expressionQuery": intent.ExpressionQuery,
		"driftLevel": intent.DriftLevel, "anchorPolicy": intent.AnchorPolicy,
	})
	if err != nil {
		return "", nil, nil, err
	}

	budget := modelDrivenToolBudget(resolved)
	for step := 0; step <= budget; step++ {
		tools := []model.ToolSpec(nil)
		if step < budget {
			tools = RespondToolSpecsForInteraction(false, resolved)
		}
		input := []model.PromptItem{
			{Type: model.PromptItemContextData, Content: `{"contextType":"character","name":"Fairy","description":"群友","textLanguage":"zh","speakingLanguage":"zh"}`},
			{Type: model.PromptItemContextData, Content: `{"contextType":"interaction","presenceProjection":"public_peer","audience":"multi","initiation":"ambient"}`},
			{Type: model.PromptItemContextData, Content: string(dialoguePayload)},
			{Type: model.PromptItemContextData, Content: string(intentPayload)},
			{Type: model.PromptItemContextData, Content: `{"contextType":"available_visual_states","states":[{"id":"idle","description":"待机"}]}`},
		}
		if social != nil && !social.Memory.Empty() {
			item, encErr := encodeSocialMemoryContext(social.Memory)
			if encErr != nil {
				return "", nil, nil, encErr
			}
			input = append(input, item)
		}
		if !retrieval.Empty() {
			payload, encErr := json.Marshal(map[string]any{
				"contextType": "retrieved_context", "knowledge": retrieval.Knowledge, "semanticStatus": retrieval.SemanticStatus,
			})
			if encErr != nil {
				return "", nil, nil, encErr
			}
			input = append(input, model.PromptItem{Type: model.PromptItemContextData, Content: string(payload)})
		}
		modelStarted := time.Now()
		events, execErr := modelPort.ExecuteRequestContext(ctx, model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{
				Lane: model.PromptLaneRespond, Model: modelName,
				Instructions: RespondInstructionsForInteraction(len(tools) > 0, resolved), MaxOutputTokens: 640,
			},
			Input: input, Tools: tools,
		})
		modelMS := time.Since(modelStarted).Milliseconds()
		if execErr != nil {
			return "", nil, phases, execErr
		}
		calls := model.FunctionCallsFromEvents(events)
		if len(calls) == 0 {
			phases = append(phases, fmt.Sprintf("model_final_ms=%d tools=%d", modelMS, len(tools)))
			return model.CollectTextFromEvents(events), toolsUsed, phases, nil
		}
		phases = append(phases, fmt.Sprintf("model_tool_round_%d_ms=%d calls=%d", step+1, modelMS, len(calls)))
		toolStarted := time.Now()
		for _, call := range calls {
			if len(toolsUsed) >= budget {
				break
			}
			query, queryErr := parseToolQuery(call.Arguments)
			if queryErr != nil {
				return "", toolsUsed, phases, queryErr
			}
			toolsUsed = append(toolsUsed, call.Name+"("+query+")")
			var extra memory.RetrievalContext
			switch call.Name {
			case toolSocialContextSearch:
				extra, err = service.selectSocialContextForTool(ctx, "character-1", "conversation-1", query)
			case toolSocialExpressionSelect:
				extra, err = service.selectSocialExpressionsForTool(ctx, "character-1", "conversation-1", query)
			case toolPublicMemorySearch:
				extra, err = service.retrievePublicKnowledgeForTool(ctx, query)
			default:
				err = fmt.Errorf("unexpected tool %s", call.Name)
			}
			if err != nil {
				return "", toolsUsed, phases, err
			}
			retrieval = mergeRetrievalContext(retrieval, extra)
		}
		phases = append(phases, fmt.Sprintf("local_tools_ms=%d", time.Since(toolStarted).Milliseconds()))
	}
	return "", toolsUsed, phases, fmt.Errorf("exhausted tool budget without final reply")
}

type livePersonaConfig struct {
	ConfigSource
	model string
}

func (c livePersonaConfig) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{Model: c.model, Capabilities: config.GatewayCapabilities{}}, nil
}
