package conversation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"fairy/agent/reply"
	"fairy/agent/tool"
	"fairy/context/social"

	"fairy/transport/session"
)

func socialMemoryQuery(intent ReplyIntent) string {
	return strings.TrimSpace(intent.MemoryQuery) + "\x00" + strings.TrimSpace(intent.ExpressionQuery)
}

func (s *Service) retrieveSocialRespondContext(ctx context.Context, characterID, conversationID string, resolved session.Resolved, intent *ReplyIntent, senderIDs []string) (*SocialRespondContext, error) {
	if intent == nil || !resolved.AllowsAmbientParticipation() || resolved.AllowsPersonalMemory() {
		return nil, nil
	}
	memoryQuery := strings.TrimSpace(intent.MemoryQuery)
	expressionQuery := strings.TrimSpace(intent.ExpressionQuery)
	if memoryQuery == "" && expressionQuery == "" {
		return nil, errors.New("public reply intent requires a social memory or expression query")
	}
	socialMemory, err := s.retrievePublicReplySocialMemory(ctx, characterID, conversationID, memoryQuery, expressionQuery)
	if err != nil {
		return nil, err
	}
	notes, err := s.memory.ambient.socialContext.ListSocialPersonNotes(ctx, characterID, conversationID, senderIDs)
	if err != nil {
		return nil, err
	}
	return &SocialRespondContext{Intent: intent, Memory: socialMemory, PersonNotes: notes}, nil
}

func (s *Service) retrievePublicReplySocialMemory(ctx context.Context, characterID, conversationID, memoryQuery, expressionQuery string) (social.SocialMemoryContext, error) {
	var memoryContext social.SocialMemoryContext
	var sharedContext social.SocialMemoryContext
	if memoryQuery != "" {
		retrieved, err := s.memory.ambient.socialRetrieval.RetrieveSocialMemoryContext(ctx, characterID, conversationID, memoryQuery)
		if err != nil {
			return social.SocialMemoryContext{}, err
		}
		sharedContext = retrieved
		memoryContext.Entries = filterSocialMemoryKinds(retrieved.Entries, social.SocialMemoryEpisode, social.SocialMemoryBehavior)
	}

	var expressionContext social.SocialMemoryContext
	if expressionQuery != "" {
		retrieved := sharedContext
		if memoryQuery == "" || expressionQuery != memoryQuery {
			var err error
			retrieved, err = s.memory.ambient.socialRetrieval.RetrieveSocialMemoryContext(ctx, characterID, conversationID, expressionQuery)
			if err != nil {
				return social.SocialMemoryContext{}, err
			}
		}
		expressionContext.Entries = filterSocialMemoryKinds(retrieved.Entries, social.SocialMemoryExpression)
	}

	merged := tool.MergeSocialMemory(memoryContext, expressionContext)
	if len(merged.Entries) > social.MaxSocialFeedbackIDs {
		merged.Entries = append([]social.SocialMemoryEntry(nil), merged.Entries[:social.MaxSocialFeedbackIDs]...)
	}
	return merged, nil
}

func filterSocialMemoryKinds(entries []social.SocialMemoryEntry, kinds ...string) []social.SocialMemoryEntry {
	if len(entries) == 0 || len(kinds) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = struct{}{}
	}
	out := make([]social.SocialMemoryEntry, 0, len(entries))
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
	regexp.MustCompile(`高性能(?:的我|的好奇心|发呆模式|机器人(?:也|都|批准|陪聊|申请|学到)?|地处理好自己的)`),
	regexp.MustCompile(`我的(判断|情感|分析|消音|学习)?模块`),
	regexp.MustCompile(`(我的|我这边的?)[^，。！？\n]{0,4}(数据库|处理器|核心存储器|内存|缓存)`),
	regexp.MustCompile(`我(数据库|核心存储器|内存|缓存)里`),
	regexp.MustCompile(`我[^，。！？\n]{0,12}(回收进|写进|存进|记到)(数据库|核心存储器|内存|缓存)`),
}

func compileReplyForInteraction(draft string, availableVisualStates []reply.VisualState, resolved session.Resolved, intent *ReplyIntent) (reply.CompiledReply, error) {
	return compileReplyForInteractionWithExpressions(draft, availableVisualStates, resolved, intent, reply.CompileOptions{})
}

func compileReplyForInteractionWithExpressions(draft string, availableVisualStates []reply.VisualState, resolved session.Resolved, intent *ReplyIntent, options reply.CompileOptions) (reply.CompiledReply, error) {
	compiled, err := reply.CompileReplyWithOptions(draft, availableVisualStates, options)
	if err != nil {
		return reply.CompiledReply{}, err
	}
	if err := validateReplyForInteraction(compiled, resolved, intent); err != nil {
		return reply.CompiledReply{}, err
	}
	return compiled, nil
}

func validateReplyForInteraction(compiled reply.CompiledReply, resolved session.Resolved, intent *ReplyIntent) error {
	if resolved.Memory == session.MemoryPublic && intent != nil {
		shape, err := publicReplyShapeForMode(intent.ReplyMode)
		if err != nil {
			return err
		}
		if len(compiled.Chains) < shape.minChains || len(compiled.Chains) > shape.maxChains {
			return &publicReplyShapeError{mode: intent.ReplyMode, actual: len(compiled.Chains), want: shape}
		}
	}
	for _, chain := range compiled.Chains {
		if chain.Kind == reply.ChainSticker {
			continue
		}
		if err := validateTextForInteraction(chain.Text, resolved); err != nil {
			return err
		}
	}
	return nil
}

func validateTextForInteraction(text string, resolved session.Resolved) error {
	if resolved.Memory != session.MemoryPublic {
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

func allowReplyPreviewForInteraction(resolved session.Resolved) bool {
	return resolved.Memory == session.MemoryPersonal
}
