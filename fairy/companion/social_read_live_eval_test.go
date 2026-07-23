//go:build live

package companion

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/secret"
)

func TestLivePublicSocialMultiStepToolUse(t *testing.T) {
	persona := loadPersonaLiveConfig(t)
	modelPort := newLiveModelPort(t, persona)

	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "ep-1", Kind: memory.SocialMemoryEpisode, Situation: "群友讨论求职准备", Content: "大家会交换整理项目经历的建议", RecallCue: "求职实习准备"},
		{ID: "ex-1", Kind: memory.SocialMemoryExpression, Situation: "安慰求职焦虑", Content: "先短句接住情绪再轻问一句进展", RecallCue: "求职焦虑安慰"},
		{ID: "bh-1", Kind: memory.SocialMemoryBehavior, Situation: "被点名求助时", Content: "先确认问题再给一个可执行小建议", RecallCue: "被点名求助"},
	}}}

	service := newSocialLearningTestService(memoryPort, modelPort)
	tools := RespondToolSpecsForInteraction(false, publicAmbientResolved())
	instructions := RespondInstructionsForInteraction(true, publicAmbientResolved())
	if strings.Contains(strings.ToLower(instructions), "use more") || strings.Contains(instructions, "must call") || strings.Contains(instructions, "always call") {
		t.Fatalf("instructions overfit tool usage: %s", instructions)
	}

	intentPayload := `{"contextType":"public_reply_intent","replyAct":"接住焦虑","tone":"自然","relationshipSignal":"平等群友","replyMode":"brief","focus":"对方求职焦虑","avoid":[],"referenceInfo":"","memoryQuery":"求职准备讨论","expressionQuery":"安慰求职焦虑","driftLevel":"active","anchorPolicy":"balanced"}`
	dialogue := `{"contextType":"dialogue","messages":[{"role":"user","text":"最近投简历好焦虑啊，感觉什么都没准备好"}]}`

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	events, err := modelPort.ExecuteRequestContext(ctx, model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneRespond, Model: persona.Model,
			Instructions: instructions, MaxOutputTokens: 640,
		},
		Input: []model.PromptItem{
			{Type: model.PromptItemContextData, Content: `{"contextType":"character","name":"Fairy","description":"群友","textLanguage":"zh"}`},
			{Type: model.PromptItemContextData, Content: `{"contextType":"interaction","presenceProjection":"public_peer","audience":"multi"}`},
			{Type: model.PromptItemContextData, Content: dialogue},
			{Type: model.PromptItemContextData, Content: intentPayload},
			{Type: model.PromptItemContextData, Content: `{"contextType":"available_visual_states","states":[{"id":"idle","description":"待机"}]}`},
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("ExecuteRequestContext: %v", err)
	}
	calls := model.FunctionCallsFromEvents(events)
	t.Logf("live tool calls: %#v", calls)
	t.Logf("live text: %q", model.CollectTextFromEvents(events))

	sawSocialTool := false
	for _, call := range calls {
		switch call.Name {
		case toolSocialContextSearch, toolSocialExpressionSelect:
			sawSocialTool = true
		}
	}
	if !sawSocialTool {
		t.Fatalf("expected at least one social tool call without expression pre-inject; got %#v", calls)
	}
	_ = service
}

func TestLiveSocialToolRetrievalRelevance(t *testing.T) {
	_ = loadPersonaLiveConfig(t) // ensure live credentials present even though retrieval is local
	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "ex-1", Kind: memory.SocialMemoryExpression, Situation: "安慰求职焦虑", Content: "先短句接住情绪再轻问一句进展", RecallCue: "求职焦虑安慰"},
		{ID: "ex-2", Kind: memory.SocialMemoryExpression, Situation: "吃饭约饭", Content: "轻松接梗问地点", RecallCue: "约饭"},
		{ID: "ep-1", Kind: memory.SocialMemoryEpisode, Situation: "群友讨论求职准备", Content: "大家会交换整理项目经历的建议", RecallCue: "求职实习准备"},
	}}}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	got, err := service.selectSocialExpressionsForTool(context.Background(), "character-1", "conversation-1", "安慰求职焦虑")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Knowledge) == 0 || got.Knowledge[0].ID != "ex-1" {
		t.Fatalf("expression select = %#v", got.Knowledge)
	}
	ctxGot, err := service.selectSocialContextForTool(context.Background(), "character-1", "conversation-1", "求职准备")
	if err != nil {
		t.Fatal(err)
	}
	if len(ctxGot.Knowledge) == 0 || ctxGot.Knowledge[0].ID != "ep-1" {
		t.Fatalf("context search = %#v", ctxGot.Knowledge)
	}
}

type personaLiveConfig struct {
	Protocol string
	BaseURL  string
	Model    string
	APIKey   string
}

func newLiveModelPort(t *testing.T, persona personaLiveConfig) *model.ModelService {
	t.Helper()
	root := t.TempDir()
	secrets := secret.NewTestStore()
	apiKey := persona.APIKey
	if _, err := config.SaveModelConnection(root, config.ModelConnectionInput{
		Protocol: persona.Protocol, Endpoint: persona.BaseURL, Model: persona.Model,
		ContextWindowTokens: 128000, AuthMode: "bearer_key",
	}, &apiKey, secrets); err != nil {
		t.Fatalf("SaveModelConnection: %v", err)
	}
	return model.NewModelService(root, secrets)
}

func loadPersonaLiveConfig(t *testing.T) personaLiveConfig {
	t.Helper()
	loadRepoDotEnv(t)
	// Prefer the local harness credential (the working shipping key) over stale PERSONA_TEST keys.
	if cfg, ok := personaConfigFromHarness(t); ok {
		t.Logf("live model source=harness model=%s endpoint=%s", cfg.Model, cfg.BaseURL)
		return cfg
	}
	if cfg, ok := personaConfigFromEnv(); ok {
		t.Logf("live model source=env model=%s endpoint=%s", cfg.Model, cfg.BaseURL)
		return cfg
	}
	t.Skip("no live model credential: set FAIRY_PERSONA_TEST_* or provide harness model/connection.json + secrets.sqlite3")
	return personaLiveConfig{}
}

func personaConfigFromEnv() (personaLiveConfig, bool) {
	cfg := personaLiveConfig{
		Protocol: strings.TrimSpace(os.Getenv("FAIRY_PERSONA_TEST_PROTOCOL")),
		BaseURL:  strings.TrimSpace(os.Getenv("FAIRY_PERSONA_TEST_BASE_URL")),
		Model:    strings.TrimSpace(os.Getenv("FAIRY_PERSONA_TEST_MODEL")),
		APIKey:   strings.TrimSpace(os.Getenv("FAIRY_PERSONA_TEST_API_KEY")),
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "chat_completions"
	}
	if cfg.BaseURL == "" || cfg.Model == "" || cfg.APIKey == "" {
		return personaLiveConfig{}, false
	}
	return cfg, true
}

func personaConfigFromHarness(t *testing.T) (personaLiveConfig, bool) {
	t.Helper()
	root := strings.TrimSpace(os.Getenv("FAIRY_CONFIG_ROOT"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return personaLiveConfig{}, false
		}
		root = filepath.Join(home, "Library", "Application Support", "dev.rinai.fairy", "harness", "v1")
	}
	connection, err := config.ReadModelConnection(root)
	if err != nil {
		t.Logf("harness connection unavailable: %v", err)
		return personaLiveConfig{}, false
	}
	dbPath := filepath.Join(root, "model", "secrets.sqlite3")
	key, err := readLegacySQLiteModelSecret(dbPath, connection.ConnectionID)
	if err != nil {
		t.Logf("harness secret unavailable: %v", err)
		return personaLiveConfig{}, false
	}
	modelName := strings.TrimSpace(os.Getenv("FAIRY_LIVE_MODEL"))
	if modelName == "" {
		modelName = strings.TrimSpace(os.Getenv("FAIRY_PERSONA_TEST_MODEL"))
	}
	if modelName == "" {
		// Live evals default to flash for latency; shipping harness may still point at pro.
		modelName = "deepseek-v4-flash"
	}
	return personaLiveConfig{
		Protocol: connection.Protocol,
		BaseURL:  connection.Endpoint,
		Model:    modelName,
		APIKey:   key,
	}, true
}

func readLegacySQLiteModelSecret(dbPath, connectionID string) (string, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return "", err
	}
	for _, r := range connectionID {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
			return "", errors.New("connection id contains unsupported characters")
		}
	}
	// Avoid adding a SQLite Go dependency (architecture forbids production sqlite).
	out, err := exec.Command("sqlite3", dbPath, "SELECT secret FROM model_secrets WHERE connection_id = '"+connectionID+"';").Output()
	if err != nil {
		return "", err
	}
	secretValue := strings.TrimSpace(string(out))
	if secretValue == "" {
		return "", errors.New("empty model secret")
	}
	return secretValue, nil
}

func loadRepoDotEnv(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 6; i++ {
		loaded := false
		for _, name := range []string{".env", ".env.persona.local"} {
			path := filepath.Join(dir, name)
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				key, value, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				key = strings.TrimSpace(key)
				value = strings.TrimSpace(value)
				value = strings.Trim(value, `"'`)
				if key == "" || os.Getenv(key) != "" {
					continue
				}
				_ = os.Setenv(key, value)
			}
			t.Logf("loaded dotenv from %s", path)
			loaded = true
		}
		if loaded {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Logf("no .env found walking up from %s", wd)
}