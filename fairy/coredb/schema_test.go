package coredb

import (
	"slices"
	"sync"
	"testing"

	gormschema "gorm.io/gorm/schema"
)

func TestPromptWindowSchemaDeclaresVersionedProjectionDefaults(t *testing.T) {
	parsed, err := gormschema.Parse(&promptWindowSchema{}, &sync.Map{}, gormschema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	revision := parsed.LookUpField("ProjectionRevision")
	if revision == nil || revision.DBName != "projection_revision" || revision.DefaultValue != "1" {
		t.Fatalf("projection revision field = %#v", revision)
	}
	state := parsed.LookUpField("ProjectionStateJSON")
	if state == nil || state.DBName != "projection_state" || !state.HasDefaultValue {
		t.Fatalf("projection state field = %#v", state)
	}
	if !slices.ContainsFunc(postgresConstraints, func(constraint schemaConstraint) bool {
		return constraint.Table == "prompt_windows" &&
			constraint.Name == "prompt_windows_projection_check"
	}) {
		t.Fatal("prompt_windows projection constraint is missing")
	}
}

func TestEmbeddingSchemaKeepsV1AndAddsNativeBGEV2(t *testing.T) {
	for _, model := range []any{&personalMemorySchema{}, &knowledgeEntrySchema{}} {
		parsed, err := gormschema.Parse(model, &sync.Map{}, gormschema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", model, err)
		}
		v1 := parsed.LookUpField("Embedding")
		if v1 == nil || v1.DBName != "embedding" || v1.TagSettings["TYPE"] != "public.vector(512)" {
			t.Fatalf("%s v1 embedding field = %#v", parsed.Table, v1)
		}
		v2 := parsed.LookUpField("EmbeddingV2")
		if v2 == nil || v2.DBName != "embedding_v2" || v2.TagSettings["TYPE"] != "public.vector(1024)" {
			t.Fatalf("%s v2 embedding field = %#v", parsed.Table, v2)
		}
		for fieldName, columnName := range map[string]string{
			"EmbeddingModelIDV2":     "embedding_model_id_v2",
			"EmbeddingContentHashV2": "embedding_content_hash_v2",
		} {
			field := parsed.LookUpField(fieldName)
			if field == nil || field.DBName != columnName {
				t.Fatalf("%s %s field = %#v", parsed.Table, fieldName, field)
			}
		}
		constraintName := parsed.Table + "_embedding_v2_check"
		if !slices.ContainsFunc(postgresConstraints, func(constraint schemaConstraint) bool {
			return constraint.Table == parsed.Table && constraint.Name == constraintName
		}) {
			t.Fatalf("%s constraint is missing", constraintName)
		}
		indexName := parsed.Table + "_embedding_v2_hnsw"
		if !slices.ContainsFunc(postgresIndexes, func(index schemaIndex) bool {
			return index.Name == indexName
		}) {
			t.Fatalf("%s index is missing", indexName)
		}
	}
}

func TestSchemaModelsDeclareOneInvariantPerTable(t *testing.T) {
	for _, model := range schemaModels() {
		parsed, err := gormschema.Parse(model, &sync.Map{}, gormschema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", model, err)
		}
		checks := parsed.ParseCheckConstraints()
		want := parsed.Table + "_invariants_check"
		if _, ok := checks[want]; !ok {
			t.Errorf("%s checks = %v, want %s", parsed.Table, checkNames(checks), want)
		}
		if len(checks) != 1 {
			t.Errorf("%s check count = %d, want 1", parsed.Table, len(checks))
		}
	}
}

func TestSchemaModelsDeclareMovedIndexes(t *testing.T) {
	var got []string
	for _, model := range schemaModels() {
		parsed, err := gormschema.Parse(model, &sync.Map{}, gormschema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", model, err)
		}
		for _, index := range parsed.ParseIndexes() {
			got = append(got, index.Name)
		}
	}
	slices.Sort(got)
	want := []string{
		"conversation_messages_conversation_role_created",
		"conversation_messages_conversation_sequence",
		"conversation_messages_conversation_sequence_key",
		"conversation_messages_turn_role_key",
		"conversation_turn_evidence_evidence",
		"conversation_turns_conversation_sequence_key",
		"conversation_turns_conversation_status",
		"conversation_turns_extraction_claim",
		"conversations_character_updated",
		"endpoint_conversations_conversation",
		"knowledge_entries_source_url",
		"knowledge_entries_status_updated",
		"personal_memories_scope_status",
		"social_memory_entries_person_note_key",
		"social_memory_entries_scope_hash_key",
		"social_memory_entries_scope_kind",
		"social_memory_feedback_events_conversation_created",
		"social_memory_feedback_events_entry_created",
		"social_memory_feedback_events_turn_entry_key",
		"stickers_status_updated",
		"tool_executions_turn_call_key",
		"tool_executions_turn_status",
		"tool_executions_turn_tool_key",
		"turn_runtime_events_conversation_turn_sequence_key",
		"turn_runtime_events_turn_sequence",
		"turn_runtime_events_type_created",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("model indexes = %v, want %v", got, want)
	}
}

func TestSocialFeedbackEventSchemaContainsOnlyAuditColumns(t *testing.T) {
	parsed, err := gormschema.Parse(&socialMemoryFeedbackEventSchema{}, &sync.Map{}, gormschema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(parsed.Fields))
	for _, field := range parsed.Fields {
		got = append(got, field.DBName)
	}
	slices.Sort(got)
	want := []string{
		"adoption", "character_id", "conversation_id", "created_at_ms", "credit",
		"entry_id", "evaluator_revision", "evidence_message_ids", "id",
		"observed_message_count", "outcome", "turn_id",
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("social feedback event columns = %v, want %v", got, want)
	}
}

func checkNames(checks map[string]gormschema.CheckConstraint) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
