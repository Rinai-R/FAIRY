package codex

import (
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestBuildDiscussPromptCarriesTeachingSceneAndResponseContract(t *testing.T) {
	t.Parallel()

	turn := app.TurnRequest{
		Session: app.Session{
			ID:                "lesson-attention:default",
			UserID:            "default",
			ActiveCharacterID: "tutor",
		},
		Character: app.Character{
			ID:          "tutor",
			DisplayName: "讲解者",
			VoiceID:     "saturn_zh_female_keainvsheng_tob",
			Persona:     "负责用 Galgame 对话讲解文档。",
			StyleRules:  []string{"只围绕当前文档教学。"},
		},
		Scene: app.Scene{
			ID:    "lesson-attention",
			Title: "文档教学：注意力机制",
			Variables: map[string]string{
				"topic":         "注意力机制",
				"learning_goal": "能解释 token 之间如何互相参考。",
			},
		},
		Prompt: app.PromptConfig{
			System:           "你是 FAIRY 的文档教学 Agent。",
			Developer:        "不要一次性生成完整剧本。",
			SceneInstruction: "文档摘要：注意力机制让模型关注输入中的重要信息。",
			ResponseContract: "speech_text 必须适合直接语音播放。",
		},
		Runtime: app.RuntimeConfig{
			Language: app.LanguagePlan{
				DisplayLanguage:     "zh-CN",
				SpeechLanguage:      "ja",
				TranslationProvider: "agent",
				Mode:                "translate_for_voice",
			},
		},
		User: app.UserInput{
			UserID: "default",
			Text:   "注意力机制到底在做什么？",
		},
	}

	prompt := buildDiscussPrompt(turn, `{"turn":{"user":{"text":"注意力机制到底在做什么？"}}}`)
	for _, want := range []string{
		"你是 FAIRY 的文档教学 Agent。",
		"不要一次性生成完整剧本。",
		"文档摘要：注意力机制让模型关注输入中的重要信息。",
		"speech_text 必须适合直接语音播放。",
		"display_text 必须使用屏幕显示语言：zh-CN",
		"speech_text 必须使用语音合成语言：ja-JP",
		"speech_text 必须是适合语音模型朗读的角色台词",
		"等义角色化转写",
		"translation_provider=agent",
		"注意力机制到底在做什么？",
		"只返回符合 schema 的 JSON 内容",
		"不要写代码，不要修改文件，不要执行命令",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestLanguageContractDefaultsToDisplayLanguage(t *testing.T) {
	t.Parallel()

	contract := languageContract(app.LanguagePlan{DisplayLanguage: "zh-CN"})
	for _, want := range []string{
		"display_text 必须使用屏幕显示语言：zh-CN",
		"speech_text 必须使用语音合成语言：zh-CN",
		"保留当前角色的性格、称呼、语气、停顿、口癖和情绪",
		"mode=translate_for_voice",
		"translation_provider=agent",
	} {
		if !strings.Contains(contract, want) {
			t.Fatalf("language contract missing %q:\n%s", want, contract)
		}
	}
}

func TestBuildActPromptDefinesLinesAsTextboxUnits(t *testing.T) {
	t.Parallel()

	prompt := buildActPrompt(agent.ActInput{
		Request: app.SceneGenerateRequest{
			Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
			Runtime: app.RuntimeConfig{
				Language: app.LanguagePlan{DisplayLanguage: "zh-CN", SpeechLanguage: "ja-JP"},
			},
		},
	}, `{"planned_node":{"id":"lesson-1"}}`)

	for _, want := range []string{
		"lines 是视觉小说文本框逐次展示的单位",
		"不是一整幕段落",
		"中文或日文单条 lines[].text 不超过 52 个可见字符",
		"英文单条 lines[].text 不超过 120 个可见字符",
		"不限制章节数量",
		"speech_text 必须与同序号 text 一一对应",
		"不能把多条字幕合并成一条语音稿",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("act prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestMakeSessionKeySeparatesCharacterUserAndSession(t *testing.T) {
	t.Parallel()

	key := makeSessionKey(app.TurnRequest{
		Session:   app.Session{ID: "lesson-1", UserID: "user-1"},
		Character: app.Character{ID: "tutor"},
		User:      app.UserInput{UserID: "user-1"},
	})
	if key != "tutor:user-1:lesson-1" {
		t.Fatalf("session key = %q", key)
	}
}
