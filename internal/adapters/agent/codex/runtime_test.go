package codex

import (
	"strings"
	"testing"

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
		"speech_text 必须使用语音合成语言：ja",
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
		"mode=same",
		"translation_provider=agent",
	} {
		if !strings.Contains(contract, want) {
			t.Fatalf("language contract missing %q:\n%s", want, contract)
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
