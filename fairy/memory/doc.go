// Package memory owns FAIRY's local conversation and long-term memory store.
//
// It provides SQLite persistence for conversations, messages, prompt windows,
// retrieval, extraction batches, personal memories, relationship memories, and
// compaction state. It does not call model providers or Wails APIs; callers pass
// validated inputs and use this package as the persistence boundary.
package memory
