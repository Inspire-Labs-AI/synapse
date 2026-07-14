package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicClient calls the Claude Messages API via the official Go SDK.
// Defaults to the current most-capable model (claude-opus-4-8); override with
// SYNAPSE_LLM_MODEL.
type AnthropicClient struct {
	client    anthropic.Client
	model     string
	maxTokens int64
}

// NewAnthropic builds a client. An empty model defaults to claude-opus-4-8.
func NewAnthropic(apiKey, model string) *AnthropicClient {
	if model == "" {
		model = anthropic.ModelClaudeOpus4_8
	}
	return &AnthropicClient{
		client:    anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:     model,
		maxTokens: 8192,
	}
}

func (a *AnthropicClient) Name() string { return "anthropic:" + a.model }

// Complete sends a single system + user turn and returns the concatenated text.
func (a *AnthropicClient) Complete(ctx context.Context, system, user string) (string, error) {
	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: a.maxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic messages: %w", err)
	}

	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), nil
}
