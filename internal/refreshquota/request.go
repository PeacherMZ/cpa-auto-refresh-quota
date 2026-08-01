package refreshquota

import (
	"encoding/json"
	"fmt"
)

type chatCompletionRequest struct {
	Model     string        `json:"model"`
	Stream    bool          `json:"stream"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func BuildRequestBody(cfg Config) ([]byte, error) {
	raw, errMarshal := json.Marshal(chatCompletionRequest{
		Model:     cfg.Model,
		Stream:    false,
		MaxTokens: cfg.MaxTokens,
		Messages: []chatMessage{{
			Role:    "user",
			Content: cfg.Message,
		}},
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal OpenAI-compatible request body: %w", errMarshal)
	}
	return raw, nil
}
