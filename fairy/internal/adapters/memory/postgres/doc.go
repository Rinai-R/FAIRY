// Package postgres owns the memory domain's PostgreSQL adapter, including Store.
//
// Layout is domain-first under adapters/memory/postgres. Repository SQL families
// live as sibling files (personal, social, extraction, conversation, compaction,
// runtime_state, ...). Store orchestration lives in store*.go siblings.
package postgres
