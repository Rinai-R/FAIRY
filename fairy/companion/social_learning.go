package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"fairy/character"
	"fairy/interaction"
	"fairy/memory"
	"fairy/model"

	"go.uber.org/zap"
)

type socialLearningSnapshot struct {
	ConversationID string
	Messages       []AmbientObservation
}

type SocialLearningStats struct {
	Enqueued  int64
	Dropped   int64
	Succeeded int64
	Failed    int64
}

type SocialLearningEngine struct {
	host   *CompanionService
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan socialLearningSnapshot
	wg     sync.WaitGroup
	once   sync.Once

	enqueued  atomic.Int64
	dropped   atomic.Int64
	succeeded atomic.Int64
	failed    atomic.Int64
}

type socialLearnPayload struct {
	Entries     json.RawMessage `json:"entries"`
	PersonNotes json.RawMessage `json:"personNotes"`
}

type socialLearnEntryDraft struct {
	Kind             string   `json:"kind"`
	Situation        string   `json:"situation"`
	Content          string   `json:"content"`
	RecallCue        string   `json:"recallCue"`
	SourceMessageIDs []string `json:"sourceMessageIds"`
}

type socialLearnPersonNoteDraft struct {
	SenderID         string   `json:"senderId"`
	Note             string   `json:"note"`
	SourceMessageIDs []string `json:"sourceMessageIds"`
}

type socialLearnCompiled struct {
	Entries []memory.SocialMemoryEntryInput
	Notes   []socialLearnCompiledPersonNote
}

type socialLearnCompiledPersonNote struct {
	SenderID   string
	SenderName string
	Note       string
}

type socialLearnObservationPayload struct {
	ContextType     string `json:"contextType"`
	MessageID       string `json:"messageId"`
	SenderID        string `json:"senderId"`
	SenderName      string `json:"senderName"`
	Text            string `json:"text"`
	TimestampUnixMS int64  `json:"timestampUnixMs"`
}

func newSocialLearningEngine(host *CompanionService, capacity int) *SocialLearningEngine {
	if capacity < 1 {
		capacity = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine := &SocialLearningEngine{host: host, ctx: ctx, cancel: cancel, queue: make(chan socialLearningSnapshot, capacity)}
	engine.wg.Add(1)
	go engine.run()
	return engine
}

func (e *SocialLearningEngine) Enqueue(snapshot socialLearningSnapshot) bool {
	if e == nil || strings.TrimSpace(snapshot.ConversationID) == "" || len(snapshot.Messages) == 0 {
		return false
	}
	snapshot.Messages = append([]AmbientObservation(nil), snapshot.Messages...)
	select {
	case <-e.ctx.Done():
		return false
	case e.queue <- snapshot:
		e.enqueued.Add(1)
		return true
	default:
		e.dropped.Add(1)
		return false
	}
}

func (e *SocialLearningEngine) Stats() SocialLearningStats {
	if e == nil {
		return SocialLearningStats{}
	}
	return SocialLearningStats{
		Enqueued: e.enqueued.Load(), Dropped: e.dropped.Load(),
		Succeeded: e.succeeded.Load(), Failed: e.failed.Load(),
	}
}

func (e *SocialLearningEngine) Close() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		e.cancel()
		e.wg.Wait()
	})
}

func (e *SocialLearningEngine) run() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case snapshot := <-e.queue:
			if err := e.process(e.ctx, snapshot); err != nil {
				e.failed.Add(1)
				if e.host != nil && e.host.logger != nil {
					e.host.logger.Warn("social learning failed", zap.String("conversationId", snapshot.ConversationID), zap.Error(err))
				}
				continue
			}
			e.succeeded.Add(1)
		}
	}
}

func (e *SocialLearningEngine) process(ctx context.Context, snapshot socialLearningSnapshot) error {
	if e.host == nil || e.host.memoryPort() == nil || e.host.modelPort() == nil || e.host.characterCatalog() == nil || e.host.configSource() == nil {
		return errors.New("social learning runtime is not configured")
	}
	resolved, err := e.host.ResolveInteraction(snapshot.ConversationID)
	if err != nil {
		return err
	}
	if !resolved.AllowsAmbientParticipation() || resolved.Memory != interaction.MemoryPublic {
		return errors.New("social learning requires a public ambient interaction")
	}
	bootstrap, err := e.host.memoryPort().LoadConversation(snapshot.ConversationID)
	if err != nil {
		return fmt.Errorf("loading social learning conversation: %w", err)
	}
	record, err := e.host.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return err
	}
	input, err := buildSocialLearningInput(record, resolved, snapshot.Messages)
	if err != nil {
		return err
	}
	connection, err := e.host.configSource().ModelConnection()
	if err != nil {
		return err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(snapshot.ConversationID, model.PromptLaneSocialLearn)
	}
	events, err := e.host.modelPort().ExecuteRequestContext(ctx, model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneSocialLearn, Model: connection.Model,
			Instructions: SocialLearnInstructions, MaxOutputTokens: SocialLearnMaxOutputTokens,
			PromptCacheKey: cacheKey,
		},
		Input: input,
	})
	if err != nil {
		return fmt.Errorf("executing social learning request: %w", err)
	}
	draft := model.CollectTextFromEvents(events)
	if strings.TrimSpace(draft) == "" {
		return emptySocialLearningResultError(events)
	}
	compiled, err := compileSocialLearning(draft, snapshot.Messages)
	if err != nil {
		return err
	}
	if len(compiled.Entries) == 0 && len(compiled.Notes) == 0 {
		return nil
	}
	if len(compiled.Entries) > 0 {
		_, err = e.host.memoryPort().StoreSocialMemoryEntries(ctx, memory.SocialMemoryBatchInput{
			CharacterID: bootstrap.Conversation.CharacterID, ConversationID: snapshot.ConversationID, Entries: compiled.Entries,
		})
		if err != nil {
			return fmt.Errorf("storing social learning entries: %w", err)
		}
	}
	for _, note := range compiled.Notes {
		_, err = e.host.memoryPort().UpsertSocialPersonNote(ctx, memory.SocialPersonNoteInput{
			CharacterID: bootstrap.Conversation.CharacterID, ConversationID: snapshot.ConversationID,
			SenderID: note.SenderID, SenderName: note.SenderName, Note: note.Note,
		})
		if err != nil {
			return fmt.Errorf("upserting social person note: %w", err)
		}
	}
	return nil
}

func emptySocialLearningResultError(events []model.StreamEvent) error {
	finishReason := "unobserved"
	completionTokens := "unobserved"
	for _, event := range events {
		switch {
		case event.Type == "completed" && strings.TrimSpace(event.FinishReason) != "":
			finishReason = strings.TrimSpace(event.FinishReason)
		case event.Type == "usage" && event.Usage != nil:
			completionTokens = fmt.Sprintf("%d", event.Usage.CompletionTokens)
		}
	}
	return fmt.Errorf("social learning result is empty: finishReason=%q completionTokens=%s", finishReason, completionTokens)
}

func socialLearningSnapshotFromState(conversationID string, state *ambientState) socialLearningSnapshot {
	if state == nil {
		return socialLearningSnapshot{ConversationID: conversationID}
	}
	start := len(state.cacheMessages) - socialLearningObservationThreshold
	if start < 0 {
		start = 0
	}
	messages := make([]AmbientObservation, 0, len(state.cacheMessages)-start)
	for _, item := range state.cacheMessages[start:] {
		observation := item.observation
		observation.IsNew = false
		observation.TraceID = ""
		messages = append(messages, observation)
	}
	return socialLearningSnapshot{ConversationID: conversationID, Messages: messages}
}

func buildSocialLearningInput(record character.Record, resolved interaction.Resolved, messages []AmbientObservation) ([]model.PromptItem, error) {
	characterItem, err := encodeCharacterContext(record)
	if err != nil {
		return nil, err
	}
	interactionItem, err := encodeInteractionContext(resolved)
	if err != nil {
		return nil, err
	}
	items := make([]model.PromptItem, 0, len(messages)+2)
	items = append(items, characterItem, interactionItem)
	for _, message := range messages {
		payload, err := json.Marshal(socialLearnObservationPayload{
			ContextType: "external_group_observation", MessageID: message.MessageID,
			SenderID: message.SenderID, SenderName: message.SenderName, Text: message.Text,
			TimestampUnixMS: message.TimestampUnixMS,
		})
		if err != nil {
			return nil, fmt.Errorf("serializing social learning observation: %w", err)
		}
		items = append(items, model.PromptItem{Type: model.PromptItemContextData, Content: string(payload)})
	}
	return items, nil
}

func compileSocialLearning(draft string, messages []AmbientObservation) (socialLearnCompiled, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(draft)))
	decoder.DisallowUnknownFields()
	var payload socialLearnPayload
	if err := decoder.Decode(&payload); err != nil {
		return socialLearnCompiled{}, fmt.Errorf("decoding social learning result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return socialLearnCompiled{}, errors.New("social learning result contains trailing data")
	}
	if len(payload.Entries) == 0 || string(payload.Entries) == "null" {
		return socialLearnCompiled{}, errors.New("social learning result requires an entries array")
	}
	entryDecoder := json.NewDecoder(strings.NewReader(string(payload.Entries)))
	entryDecoder.DisallowUnknownFields()
	var drafts []socialLearnEntryDraft
	if err := entryDecoder.Decode(&drafts); err != nil {
		return socialLearnCompiled{}, fmt.Errorf("decoding social learning entries: %w", err)
	}
	if len(drafts) > maxSocialLearningEntries {
		return socialLearnCompiled{}, fmt.Errorf("social learning result must contain at most %d entries", maxSocialLearningEntries)
	}
	messageByID := make(map[string]AmbientObservation, len(messages))
	for _, message := range messages {
		messageByID[message.MessageID] = message
	}
	entries := make([]memory.SocialMemoryEntryInput, 0, len(drafts))
	for index, item := range drafts {
		entry, err := compileSocialLearningEntry(index, item, messageByID)
		if err != nil {
			return socialLearnCompiled{}, err
		}
		entries = append(entries, entry)
	}
	notes, err := compileSocialLearningPersonNotes(payload.PersonNotes, messageByID)
	if err != nil {
		return socialLearnCompiled{}, err
	}
	return socialLearnCompiled{Entries: entries, Notes: notes}, nil
}

func compileSocialLearningPersonNotes(raw json.RawMessage, messages map[string]AmbientObservation) ([]socialLearnCompiledPersonNote, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if string(raw) == "null" {
		return nil, errors.New("social learning result personNotes must not be null")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var drafts []socialLearnPersonNoteDraft
	if err := decoder.Decode(&drafts); err != nil {
		return nil, fmt.Errorf("decoding social learning personNotes: %w", err)
	}
	if len(drafts) > maxSocialLearningPersonNotes {
		return nil, fmt.Errorf("social learning result must contain at most %d personNotes", maxSocialLearningPersonNotes)
	}
	senders := make(map[string]string, len(messages))
	for _, message := range messages {
		if message.SenderID == "" {
			continue
		}
		if _, exists := senders[message.SenderID]; !exists {
			senders[message.SenderID] = message.SenderName
		}
	}
	notes := make([]socialLearnCompiledPersonNote, 0, len(drafts))
	seenSender := make(map[string]struct{}, len(drafts))
	for index, item := range drafts {
		note, err := compileSocialLearningPersonNote(index, item, messages, senders)
		if err != nil {
			return nil, err
		}
		if _, exists := seenSender[note.SenderID]; exists {
			return nil, fmt.Errorf("social learning personNote %d duplicates senderId", index)
		}
		seenSender[note.SenderID] = struct{}{}
		notes = append(notes, note)
	}
	return notes, nil
}

func compileSocialLearningPersonNote(
	index int,
	item socialLearnPersonNoteDraft,
	messages map[string]AmbientObservation,
	senders map[string]string,
) (socialLearnCompiledPersonNote, error) {
	senderID := strings.TrimSpace(item.SenderID)
	if senderID == "" || senderID != item.SenderID {
		return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d senderId is invalid", index)
	}
	senderName, known := senders[senderID]
	if !known {
		return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d references an unknown sender", index)
	}
	note := strings.TrimSpace(item.Note)
	if note == "" || note != item.Note || utf8.RuneCountInString(note) > memory.MaxSocialPersonNoteRunes {
		return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d note is invalid", index)
	}
	for _, r := range note {
		if unicode.IsControl(r) {
			return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d note contains control characters", index)
		}
	}
	if len(item.SourceMessageIDs) == 0 || len(item.SourceMessageIDs) > maxSocialLearningSourceIDs {
		return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d sourceMessageIds count is invalid", index)
	}
	seen := make(map[string]struct{}, len(item.SourceMessageIDs))
	fromSender := false
	for _, id := range item.SourceMessageIDs {
		if _, exists := seen[id]; exists {
			return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d contains duplicate source IDs", index)
		}
		seen[id] = struct{}{}
		message, exists := messages[id]
		if !exists {
			return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d references an unknown source message", index)
		}
		if message.SenderID == senderID {
			fromSender = true
		}
		if containsLongSocialQuote(note, message.Text) {
			return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d copies a long source passage", index)
		}
	}
	if !fromSender {
		return socialLearnCompiledPersonNote{}, fmt.Errorf("social learning personNote %d must cite at least one message from the sender", index)
	}
	return socialLearnCompiledPersonNote{SenderID: senderID, SenderName: senderName, Note: note}, nil
}

func compileSocialLearningEntry(index int, item socialLearnEntryDraft, messages map[string]AmbientObservation) (memory.SocialMemoryEntryInput, error) {
	if item.Kind != memory.SocialMemoryEpisode && item.Kind != memory.SocialMemoryExpression && item.Kind != memory.SocialMemoryBehavior {
		return memory.SocialMemoryEntryInput{}, fmt.Errorf("social learning entry %d kind is invalid", index)
	}
	for name, value := range map[string]string{"situation": item.Situation, "content": item.Content, "recallCue": item.RecallCue} {
		limit := memory.MaxSocialContentRunes
		switch name {
		case "situation":
			limit = memory.MaxSocialSituationRunes
		case "recallCue":
			limit = memory.MaxSocialRecallRunes
		}
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > limit {
			return memory.SocialMemoryEntryInput{}, fmt.Errorf("social learning entry %d %s is invalid", index, name)
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return memory.SocialMemoryEntryInput{}, fmt.Errorf("social learning entry %d %s contains control characters", index, name)
			}
		}
	}
	if len(item.SourceMessageIDs) == 0 || len(item.SourceMessageIDs) > maxSocialLearningSourceIDs {
		return memory.SocialMemoryEntryInput{}, fmt.Errorf("social learning entry %d sourceMessageIds count is invalid", index)
	}
	seen := make(map[string]struct{}, len(item.SourceMessageIDs))
	var start, end int64
	for _, id := range item.SourceMessageIDs {
		if _, exists := seen[id]; exists {
			return memory.SocialMemoryEntryInput{}, fmt.Errorf("social learning entry %d contains duplicate source IDs", index)
		}
		seen[id] = struct{}{}
		message, exists := messages[id]
		if !exists {
			return memory.SocialMemoryEntryInput{}, fmt.Errorf("social learning entry %d references an unknown source message", index)
		}
		if start == 0 || message.TimestampUnixMS < start {
			start = message.TimestampUnixMS
		}
		if message.TimestampUnixMS > end {
			end = message.TimestampUnixMS
		}
		if containsLongSocialQuote(item.Content, message.Text) {
			return memory.SocialMemoryEntryInput{}, fmt.Errorf("social learning entry %d copies a long source passage", index)
		}
	}
	return memory.SocialMemoryEntryInput{
		Kind: item.Kind, Situation: item.Situation, Content: item.Content, RecallCue: item.RecallCue,
		SourceStartUnixMS: start, SourceEndUnixMS: end,
	}, nil
}

func containsLongSocialQuote(candidate, source string) bool {
	const quoteRunes = 24
	candidate = strings.Join(strings.Fields(candidate), "")
	sourceRunes := []rune(strings.Join(strings.Fields(source), ""))
	if len(sourceRunes) < quoteRunes {
		return false
	}
	for index := 0; index+quoteRunes <= len(sourceRunes); index++ {
		if strings.Contains(candidate, string(sourceRunes[index:index+quoteRunes])) {
			return true
		}
	}
	return false
}
