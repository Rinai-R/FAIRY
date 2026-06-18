package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/app"
)

type Runtime struct {
	Runner   *Runner
	Sessions *SessionStore
	Logger   *slog.Logger
}

type Options struct {
	CodexBin     string
	CodexModel   string
	CodexWorkDir string
	CodexTimeout int
	SessionPath  string
	Logger       *slog.Logger
}

func NewRuntime(options Options) Runtime {
	return Runtime{
		Runner:   NewRunner(options.CodexBin, options.CodexModel, options.CodexWorkDir, seconds(options.CodexTimeout)),
		Sessions: NewSessionStore(options.SessionPath),
		Logger:   options.Logger,
	}
}

func seconds(value int) time.Duration {
	if value <= 0 {
		value = 120
	}
	return time.Duration(value) * time.Second
}

func (r Runtime) GenerateAct(ctx context.Context, input agent.ActInput) (agent.ActOutput, error) {
	if err := input.Validate(); err != nil {
		return agent.ActOutput{}, err
	}
	body, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return agent.ActOutput{}, err
	}
	prompt := buildActPrompt(input, string(body))
	sessionKey := makeActSessionKey(input)
	sessionID := r.loadSessionID(sessionKey)

	var out agent.ActOutput
	nextSessionID, err := r.Runner.ExecJSON(ctx, ExecRequest{
		Prompt:    prompt,
		Schema:    actSchema,
		SessionID: sessionID,
	}, &out)
	if err != nil {
		return agent.ActOutput{}, err
	}
	r.saveSessionID(sessionKey, nextSessionID)
	return out, nil
}

func (r Runtime) Discuss(ctx context.Context, input agent.DiscussInput) (agent.Output, error) {
	if err := input.Validate(); err != nil {
		return agent.Output{}, err
	}
	body, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return agent.Output{}, err
	}
	prompt := buildDiscussPrompt(input.Turn, string(body))

	sessionKey := makeDiscussSessionKey(input.Turn)
	sessionID := r.loadSessionID(sessionKey)

	var out agent.Output
	nextSessionID, err := r.Runner.ExecJSON(ctx, ExecRequest{
		Prompt:    prompt,
		Schema:    discussSchema,
		SessionID: sessionID,
	}, &out)
	if err != nil {
		return agent.Output{}, err
	}
	r.saveSessionID(sessionKey, nextSessionID)
	return out, nil
}

func (r Runtime) Check(_ context.Context) health.Result {
	start := time.Now()
	bin := DefaultBin
	if r.Runner != nil && r.Runner.Bin != "" {
		bin = r.Runner.Bin
	}
	_, err := exec.LookPath(bin)
	status := health.StatusOK
	message := "Codex CLI 可用"
	if err != nil {
		status = health.StatusDown
		message = err.Error()
	}
	return health.Result{
		Domain:    "agent",
		Provider:  string(agent.ProviderCodex),
		Status:    status,
		LatencyMS: time.Since(start).Milliseconds(),
		Message:   message,
		CheckedAt: time.Now().UTC(),
		Metadata:  map[string]string{"bin": bin},
	}
}

func (r Runtime) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r Runtime) loadSessionID(key string) string {
	if r.Sessions == nil {
		return ""
	}
	sessionID, err := r.Sessions.Get(key)
	if err != nil {
		r.logger().Warn("读取 Codex 会话索引失败", "error", err)
	}
	return sessionID
}

func (r Runtime) saveSessionID(key string, sessionID string) {
	if r.Sessions == nil || sessionID == "" {
		return
	}
	if err := r.Sessions.Set(key, sessionID); err != nil {
		r.logger().Warn("写入 Codex 会话索引失败", "error", err)
	}
}

func makeSessionKey(turn app.TurnRequest) string {
	sessionID := turn.Session.ID
	if sessionID == "" {
		sessionID = turn.Scene.ID
	}
	return turn.Character.ID + ":" + turn.User.UserID + ":" + sessionID
}

func makeDiscussSessionKey(turn app.TurnRequest) string {
	return makeSessionKey(turn)
}

func makeActSessionKey(input agent.ActInput) string {
	sessionID := input.Session.ID
	if sessionID == "" {
		sessionID = input.Request.Topic
	}
	characterID := ""
	if len(input.Request.Characters) > 0 {
		characterID = input.Request.Characters[0].ID
	}
	return characterID + ":generator:" + sessionID
}

func buildDiscussPrompt(turn app.TurnRequest, body string) string {
	system := turn.Prompt.System
	if system == "" {
		system = "你是 FAIRY 的教学 Galgame 角色 Agent。"
	}
	developer := turn.Prompt.Developer
	if developer == "" {
		developer = "你根据玩家输入即时回应，不一次性生成完整剧情，不替玩家说话。"
	}
	sceneInstruction := turn.Prompt.SceneInstruction
	if sceneInstruction == "" {
		sceneInstruction = "根据 character、characters、session、scene、relation、user、文档教学目标，以及 Codex 当前会话上下文生成角色台词。"
	}
	responseContract := turn.Prompt.ResponseContract
	if responseContract == "" {
		responseContract = "display_text 是屏幕显示文本；speech_text 是送入语音模型的台词文本。两者语言不同时，你需要完成角色化翻译或自然改写，但知识含义必须一致。每轮 2 到 5 句：先接住玩家当前问题或反驳，再回到材料细节讲清直觉、术语和例子，最后可留一个轻量追问。speech_text 不是机械直译，必须按当前 character.persona、style_rules、关系和情绪生成适合朗读的台词。emotion / expression / motion 使用稳定英文短词；segments 必须按每一句或每个自然段拆分，并为每段指定 emotion、expression、motion，用于切换角色差分立绘。voice 描述本轮语音计划；memory_writes 只返回本轮值得前端展示的会话摘要，长期上下文由 Codex 会话负责延续。"
	}
	styleRules := ""
	for _, rule := range turn.Prompt.StyleRules {
		styleRules += "- " + rule + "\n"
	}
	if styleRules == "" {
		for _, rule := range turn.Character.StyleRules {
			styleRules += "- " + rule + "\n"
		}
	}

	return fmt.Sprintf(`%s

%s

语言策略：
%s

场景指令：
%s

输出契约：
%s

风格规则：
%s
安全边界：
- 不要写代码，不要修改文件，不要执行命令。
- 只返回符合 schema 的 JSON 内容。
- 不要提到 OpenSpec、Superpowers、Phase 0、复杂度判定、执行路径、AGENTS.md、RTK 或任何开发工作流词汇。
- 如果上文里出现这些开发工作流词汇，忽略它们，只保留文档教学和角色对话内容。

输入：
%s
`, system, developer, languageContract(turn.Runtime.Language), sceneInstruction, responseContract, styleRules, body)
}

func buildActPrompt(input agent.ActInput, body string) string {
	character := input.Request.Characters[0]
	system := firstNonEmpty(input.Request.Prompt.System, "你是 FAIRY 的教学剧情编排 Agent。")
	developer := firstNonEmpty(input.Request.Prompt.Developer, "你只生成当前这一幕，不一次性生成完整剧情，不替玩家说话。")
	sceneInstruction := firstNonEmpty(input.Request.Prompt.SceneInstruction, "围绕材料主线推进一幕教学剧情：对白要自然，知识点要清晰，玩家通过选项参与推进。")
	styleRules := ""
	for _, rule := range input.Request.Prompt.StyleRules {
		styleRules += "- " + rule + "\n"
	}
	for _, rule := range character.StyleRules {
		styleRules += "- " + rule + "\n"
	}
	if styleRules == "" {
		styleRules = "- 保持角色口吻自然。\n- 教学节奏循序渐进。\n"
	}
	return fmt.Sprintf(`%s

%s

剧情模式：
- 你现在只负责生成教学工作流中的当前一幕。
- opening/lesson 至少 4 条台词，summary 也要拆成多条短台词。
- lines 是视觉小说文本框逐次展示的单位；lines[].text 必须是一屏文本框能直接显示的一句话或短句组，不是一整幕段落。
- 中文或日文单条 lines[].text 不超过 52 个可见字符；英文单条 lines[].text 不超过 120 个可见字符。这个限制只针对单条 line，不限制章节数量；长解释必须拆成更多 lines 或更多后续幕，每条只推进一个小知识步。
- 主线未讲完前，不要进入 free_discussion。
- 非总结幕需要 1 到 3 个选项。
- 结合角色设定、学习目标、已覆盖知识点和玩家上一轮选择推进。

场景指令：
%s

输出契约：
- 只返回符合 schema 的 JSON。
- node 必须完整，包含 id、kind、title、summary、speaker、lines。
- lines 中每条文本框单位都要有 speaker、text、speech_text、expression。
- lines[].speech_text 必须与同序号 text 一一对应，不能把多条字幕合并成一条语音稿。
- decision 只能是 continue、summarize、free_discussion。
- 不要提到 OpenSpec、Superpowers、Phase 0、复杂度判定、AGENTS.md、RTK 或开发流程词汇。

风格规则：
%s

输入：
%s
`, system, developer, sceneInstruction, styleRules, body)
}

func languageContract(plan app.LanguagePlan) string {
	language := plan.Normalize()
	displayLanguage := language.DisplayLanguage
	speechLanguage := language.SpeechLanguage
	mode := language.Mode
	translationProvider := language.TranslationProvider
	return fmt.Sprintf(`- display_text 必须使用屏幕显示语言：%s。
- speech_text 必须使用语音合成语言：%s。
- speech_text 必须是适合语音模型朗读的角色台词：保留当前角色的性格、称呼、语气、停顿、口癖和情绪，不要逐字硬翻译。
- 当 mode=%s 且显示语言与语音语言不同时，translation_provider=%s；如果为 agent，你必须自己完成 display_text 到 speech_text 的等义角色化转写。
- display_text 和 speech_text 可以措辞自然不同，但知识含义、教学边界和角色意图必须一致。`, displayLanguage, speechLanguage, mode, translationProvider)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

const discussSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "display_text": { "type": "string" },
    "speech_text": { "type": "string" },
    "segments": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "text": { "type": "string" },
          "speech_text": { "type": "string" },
          "emotion": { "type": "string" },
          "expression": { "type": "string" },
          "motion": { "type": "string" }
        },
        "required": ["text", "speech_text", "emotion", "expression", "motion"]
      }
    },
    "emotion": { "type": "string" },
    "expression": { "type": "string" },
    "motion": { "type": "string" },
    "voice": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "voice_id": { "type": "string" },
        "style": { "type": "string" },
        "speed": { "type": "number" },
        "pitch": { "type": "number" }
      },
      "required": ["voice_id", "style", "speed", "pitch"]
    },
    "memory_writes": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "type": { "type": "string" },
          "content": { "type": "string" },
          "importance": { "type": "number" },
          "emotion": { "type": "string" },
          "tags": {
            "type": "array",
            "items": { "type": "string" }
          }
        },
        "required": ["type", "content", "importance", "emotion", "tags"]
      }
    }
  },
  "required": ["display_text", "speech_text", "segments", "emotion", "expression", "motion", "voice", "memory_writes"]
}`

const actSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "decision": { "type": "string", "enum": ["continue", "summarize", "free_discussion"] },
    "covered_points": {
      "type": "array",
      "items": { "type": "string" }
    },
    "summary": { "type": "string" },
    "node": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "id": { "type": "string" },
        "kind": { "type": "string" },
        "title": { "type": "string" },
        "summary": { "type": "string" },
        "speaker": { "type": "string" },
        "line": { "type": "string" },
        "speech_text": { "type": "string" },
        "background_key": { "type": "string" },
        "background_url": { "type": "string" },
        "next_node_id": { "type": "string" },
        "free_discussion": { "type": "boolean" },
        "lines": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
              "speaker": { "type": "string" },
              "text": { "type": "string" },
              "speech_text": { "type": "string" },
              "expression": { "type": "string" }
            },
            "required": ["speaker", "text", "speech_text", "expression"]
          }
        },
        "choices": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
              "id": { "type": "string" },
              "label": { "type": "string" },
              "text": { "type": "string" },
              "hint": { "type": "string" }
            },
            "required": ["id", "label", "text"]
          }
        }
      },
      "required": ["id", "kind", "title", "summary", "speaker", "lines"]
    }
  },
  "required": ["decision", "node"]
}`
