package character

// VisualState is an available character presentation state for Prompt and reply
// compilation. It is domain data, not a delivery mechanism.
type VisualState struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}
