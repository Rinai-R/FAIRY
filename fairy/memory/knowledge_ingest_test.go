package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

func validKnowledgeIngestTask() KnowledgeIngestTask {
	return KnowledgeIngestTask{
		ID: "task-1", ConversationID: "conversation-1", TurnID: "turn-1",
		Source: KnowledgeIngestSource{
			ID: "source-1", Title: "作品标题", URL: "https://example.test/item?id=1",
			Snippet: "这是一条足够完整的公开来源摘要。", Rank: 1, FetchedAtUnixMS: 1,
		},
	}
}

func TestValidateKnowledgeIngestTaskBoundsCanonicalSource(t *testing.T) {
	valid := validKnowledgeIngestTask()
	encoded, err := validateKnowledgeIngestTask(valid)
	if err != nil || len(encoded) == 0 || encoded[0] != '{' {
		t.Fatalf("valid task = (%q, %v)", encoded, err)
	}
	var source KnowledgeIngestSource
	if err := json.Unmarshal(encoded, &source); err != nil || source != valid.Source {
		t.Fatalf("encoded source = (%#v, %v)", source, err)
	}
	tests := map[string]func(*KnowledgeIngestTask){
		"empty source":   func(task *KnowledgeIngestTask) { task.Source = KnowledgeIngestSource{} },
		"fragment URL":   func(task *KnowledgeIngestTask) { task.Source.URL += "#fragment" },
		"credential URL": func(task *KnowledgeIngestTask) { task.Source.URL = "https://user:pass@example.test/item" },
		"invalid rank":   func(task *KnowledgeIngestTask) { task.Source.Rank = 0 },
		"long snippet": func(task *KnowledgeIngestTask) {
			task.Source.Snippet = strings.Repeat("长", MaxKnowledgeIngestSnippetRunes+1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			task := valid
			mutate(&task)
			if _, err := validateKnowledgeIngestTask(task); err == nil {
				t.Fatalf("expected invalid task: %#v", task)
			}
		})
	}
}

func TestEnqueueKnowledgeIngestTasksRejectsInvalidTaskBeforeDatabaseAccess(t *testing.T) {
	store := &Store{}
	valid := validKnowledgeIngestTask()
	invalid := valid
	invalid.ID = "task-2"
	invalid.Source.ID = ""
	err := store.enqueueKnowledgeIngestTasksPostgres(t.Context(), []KnowledgeIngestTask{valid, invalid})
	if err == nil || !strings.Contains(err.Error(), "task[1]") {
		t.Fatalf("error = %v", err)
	}
}

func TestKnowledgeIngestTaskFromJobDecodesCurrentPayload(t *testing.T) {
	valid := validKnowledgeIngestTask()
	payload, err := json.Marshal(valid.Source)
	if err != nil {
		t.Fatal(err)
	}
	task, err := knowledgeIngestTaskFromJob(KnowledgeIngestJob{
		ID:             "job-1",
		ConversationID: valid.ConversationID,
		TurnID:         valid.TurnID,
		TaskID:         valid.ID,
		SourceJSON:     payload,
	})
	if err != nil || task != valid {
		t.Fatalf("task = (%#v, %v)", task, err)
	}
}

func TestKnowledgeIngestTaskFromJobRejectsSourceCollections(t *testing.T) {
	valid := validKnowledgeIngestTask()
	second := valid.Source
	second.ID = "source-2"
	second.URL = "https://example.test/item?id=2"
	payload, err := json.Marshal([]KnowledgeIngestSource{valid.Source, second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = knowledgeIngestTaskFromJob(KnowledgeIngestJob{
		ID:             "job-1",
		ConversationID: valid.ConversationID,
		TurnID:         valid.TurnID,
		TaskID:         valid.ID,
		SourceJSON:     payload,
	})
	if err == nil || !strings.Contains(err.Error(), "source JSON is invalid") {
		t.Fatalf("error = %v", err)
	}
}

func TestKnowledgeIngestTaskFromJobRequiresCurrentTaskIdentity(t *testing.T) {
	valid := validKnowledgeIngestTask()
	payload, err := json.Marshal(valid.Source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = knowledgeIngestTaskFromJob(KnowledgeIngestJob{
		ID:             "job-legacy",
		ConversationID: valid.ConversationID,
		TurnID:         valid.TurnID,
		SourceJSON:     payload,
	})
	if err == nil || !strings.Contains(err.Error(), "task ID is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestKnowledgeIngestJobRecordProjectsOnlyTaskIdentity(t *testing.T) {
	encoded, err := json.Marshal(KnowledgeIngestJobRecord{
		ID: "job-1", TaskID: "task-1", Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	if !strings.Contains(payload, `"taskId":"task-1"`) || strings.Contains(payload, "batchId") {
		t.Fatalf("job record JSON = %s", payload)
	}
}
