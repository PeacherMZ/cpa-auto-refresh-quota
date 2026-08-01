package refreshquota

import (
	"encoding/json"
	"testing"
)

func TestBuildRequestBodyPreservesConfiguredMessage(t *testing.T) {
	cfg := Config{Model: "model-1", Message: "  exact message\n", MaxTokens: 7}
	raw, err := BuildRequestBody(cfg)
	if err != nil {
		t.Fatalf("BuildRequestBody() error = %v", err)
	}
	var body struct {
		Model     string        `json:"model"`
		Stream    bool          `json:"stream"`
		MaxTokens int           `json:"max_tokens"`
		Messages  []chatMessage `json:"messages"`
	}
	if errDecode := json.Unmarshal(raw, &body); errDecode != nil {
		t.Fatalf("decode request body: %v", errDecode)
	}
	if body.Model != cfg.Model || body.Stream || body.MaxTokens != cfg.MaxTokens {
		t.Fatalf("request body = %#v", body)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != cfg.Message {
		t.Fatalf("messages = %#v, want exact configured message", body.Messages)
	}
}
