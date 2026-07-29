package coredb

import (
	"slices"
	"sync"
	"testing"

	gormschema "gorm.io/gorm/schema"
)

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
		"conversations_character_updated",
		"endpoint_conversations_conversation",
		"feedback_events_claim",
		"feedback_events_group",
		"feedback_events_scope",
		"knowledge_documents_canonical_key",
		"knowledge_entries_document",
		"knowledge_entries_status_updated",
		"personal_memories_scope_status",
		"social_memory_entries_person_note_key",
		"social_memory_entries_scope_hash_key",
		"social_memory_entries_scope_kind",
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

func checkNames(checks map[string]gormschema.CheckConstraint) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
