package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Message struct {
	Role    string
	Content string
}

type Profile struct {
	Endpoint  string
	Model     string
	APIKey    string
	ExtraBody string
}

type Request struct {
	Profile     Profile
	Messages    []Message
	Temperature float64
}

func (request Request) Validate() error {
	if len(request.Messages) == 0 {
		return errors.New("messages 不能为空")
	}
	for index, message := range request.Messages {
		if strings.TrimSpace(message.Role) == "" {
			return fmt.Errorf("messages[%d].role 不能为空", index)
		}
		if strings.TrimSpace(message.Content) == "" {
			return fmt.Errorf("messages[%d].content 不能为空", index)
		}
	}
	return nil
}

type Adapter interface {
	Validate(profile Profile) error
	CompleteJSON(ctx context.Context, request Request) (string, error)
}
