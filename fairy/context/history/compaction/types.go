package compaction

type Result struct {
	WindowRevision        uint64 `json:"windowRevision"`
	RetainedDialogueItems int    `json:"retainedDialogueItems"`
}
