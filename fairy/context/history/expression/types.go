package expression

type Kind string

const (
	Utterance Kind = "utterance"
	Sticker   Kind = "sticker"
)

type StickerSnapshot struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType"`
}

type Part struct {
	Kind        Kind             `json:"kind"`
	Text        string           `json:"text,omitempty"`
	VisualState string           `json:"visualState"`
	Sticker     *StickerSnapshot `json:"sticker,omitempty"`
}
