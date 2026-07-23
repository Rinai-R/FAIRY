package companion

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"fairy/interaction"
	"fairy/memory"
)

func socialMemoryQuery(intent ReplyIntent) string {
	return strings.TrimSpace(intent.MemoryQuery)
}

func (s *CompanionService) retrieveSocialRespondContext(ctx context.Context, characterID, conversationID string, resolved interaction.Resolved, intent *ReplyIntent, senderIDs []string) (*SocialRespondContext, error) {
	if intent == nil || !resolved.AllowsAmbientParticipation() || resolved.AllowsPersonalMemory() {
		return nil, nil
	}
	memoryQuery := strings.TrimSpace(intent.MemoryQuery)
	expressionQuery := strings.TrimSpace(intent.ExpressionQuery)
	if memoryQuery == "" && expressionQuery == "" {
		return nil, errors.New("public reply intent requires a social memory or expression query")
	}
	var socialMemory memory.SocialMemoryContext
	if memoryQuery != "" {
		retrieved, err := s.memoryPort().RetrieveSocialMemoryContext(ctx, characterID, conversationID, memoryQuery)
		if err != nil {
			return nil, err
		}
		socialMemory.Entries = filterSocialMemoryKinds(retrieved.Entries, memory.SocialMemoryEpisode, memory.SocialMemoryBehavior)
	}
	notes, err := s.memoryPort().ListSocialPersonNotes(ctx, characterID, conversationID, senderIDs)
	if err != nil {
		return nil, err
	}
	return &SocialRespondContext{Intent: intent, Memory: socialMemory, PersonNotes: notes}, nil
}

func filterSocialMemoryKinds(entries []memory.SocialMemoryEntry, kinds ...string) []memory.SocialMemoryEntry {
	if len(entries) == 0 || len(kinds) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = struct{}{}
	}
	out := make([]memory.SocialMemoryEntry, 0, len(entries))
	for _, entry := range entries {
		if _, ok := allowed[entry.Kind]; ok {
			out = append(out, entry)
		}
	}
	return out
}

var errPublicPeerIdentity = errors.New("public reply violates peer identity boundary")

type publicReplyShape struct {
	minChains int
	maxChains int
}

func publicReplyShapeForMode(mode string) (publicReplyShape, error) {
	switch mode {
	case "brief":
		return publicReplyShape{minChains: 1, maxChains: 1}, nil
	case "normal":
		return publicReplyShape{minChains: 1, maxChains: 3}, nil
	case "expanded":
		return publicReplyShape{minChains: 1, maxChains: 5}, nil
	default:
		return publicReplyShape{}, fmt.Errorf("public reply mode %q is invalid", mode)
	}
}

type publicReplyShapeError struct {
	mode   string
	actual int
	want   publicReplyShape
}

func (e *publicReplyShapeError) Error() string {
	return fmt.Sprintf("public reply mode %q requires %d-%d chains, got %d", e.mode, e.want.minChains, e.want.maxChains, e.actual)
}

var publicPeerIdentityPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(我是|我也是|我也算是|虽然我是|作为|身为)[^，。！？\n]{0,8}(机器人|人工智能|ai|bot|model|assistant|system)`),
	regexp.MustCompile(`(?i)\b(i am|i'm|as an?|being an?)\s+(an?\s+)?(ai|bot|robot|model|assistant|system)\b`),
	regexp.MustCompile(`(?i)(私は|僕は|俺は|として)[^、。！？\n]{0,8}(ai|bot|ロボット|モデル|アシスタント|システム)`),
	regexp.MustCompile(`高性能[^，。！？\n]{0,8}(机器人|模块|模式|学习成果|判断|可不是白叫|不是白叫)`),
	regexp.MustCompile(`我的(判断|情感|分析|消音|学习)?模块`),
	regexp.MustCompile(`(我的|我这边的?)[^，。！？\n]{0,4}(数据库|处理器|核心存储器|内存|缓存)`),
	regexp.MustCompile(`我(数据库|核心存储器|内存|缓存)里`),
	regexp.MustCompile(`我[^，。！？\n]{0,12}(回收进|写进|存进|记到)(数据库|核心存储器|内存|缓存)`),
}

func compileReplyForInteraction(draft string, availableVisualStates []VisualState, resolved interaction.Resolved, intent *ReplyIntent) (CompiledReply, error) {
	reply, err := CompileReply(draft, availableVisualStates)
	if err != nil {
		return CompiledReply{}, err
	}
	if err := validateReplyForInteraction(reply, resolved, intent); err != nil {
		return CompiledReply{}, err
	}
	return reply, nil
}

func validateReplyForInteraction(reply CompiledReply, resolved interaction.Resolved, intent *ReplyIntent) error {
	if resolved.Memory == interaction.MemoryPublic && intent != nil {
		shape, err := publicReplyShapeForMode(intent.ReplyMode)
		if err != nil {
			return err
		}
		if len(reply.Chains) < shape.minChains || len(reply.Chains) > shape.maxChains {
			return &publicReplyShapeError{mode: intent.ReplyMode, actual: len(reply.Chains), want: shape}
		}
	}
	for _, chain := range reply.Chains {
		if err := validateTextForInteraction(chain.Text, resolved); err != nil {
			return err
		}
	}
	return nil
}

func validateTextForInteraction(text string, resolved interaction.Resolved) error {
	if resolved.Memory != interaction.MemoryPublic {
		return nil
	}
	return validatePublicPeerText(text)
}

func validatePublicPeerText(text string) error {
	for _, pattern := range publicPeerIdentityPatterns {
		if pattern.MatchString(text) {
			return errPublicPeerIdentity
		}
	}
	return nil
}

func replyCompileRetryCorrection(err error) string {
	if errors.Is(err, errPublicPeerIdentity) {
		return " RETRY CORRECTION: The previous reply violated the public peer identity boundary. Rewrite it as an ordinary human peer without any robot, AI, system, model, assistant, high-performance-machine, module, database, processor, memory, or cache self-description. Return only the required strict reply JSON."
	}
	var shapeErr *publicReplyShapeError
	if errors.As(err, &shapeErr) {
		return fmt.Sprintf(" RETRY CORRECTION: The previous public reply used %d chains, but replyMode %q requires %d-%d chains. Keep the same target and one conversational hook, then return only the required strict reply JSON.", shapeErr.actual, shapeErr.mode, shapeErr.want.minChains, shapeErr.want.maxChains)
	}
	return " RETRY CORRECTION: The previous reply did not satisfy the strict reply protocol. Return a newly generated reply as exactly one valid JSON object matching the required schema, with no prose, Markdown, unknown fields, or trailing data."
}

func allowReplyPreviewForInteraction(resolved interaction.Resolved) bool {
	return resolved.Memory == interaction.MemoryPersonal
}
