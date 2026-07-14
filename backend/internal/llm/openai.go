package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIChatClient calls the OpenAI chat completions API (default gpt-4o) over
// raw HTTP, requesting a JSON object response.
type OpenAIChatClient struct {
	apiKey   string
	base     string
	model    string
	provider string // display label for Name(): openai | openrouter | ollama
	http     *http.Client
}

// NewOpenAIChat builds a client for any OpenAI-compatible chat endpoint (OpenAI,
// OpenRouter, or local Ollama). base/model default if empty; label is the
// provider name shown in logs (defaults to "openai").
func NewOpenAIChat(apiKey, base, model, label string) *OpenAIChatClient {
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o"
	}
	if label == "" {
		label = "openai"
	}
	return &OpenAIChatClient{
		apiKey:   apiKey,
		base:     base,
		model:    model,
		provider: label,
		// Local CPU inference (e.g. qwen2.5-coder via Ollama, no GPU) can take a
		// minute or more — and several minutes on a cold model load. Keep this
		// generous so slow completions return a 200 answer instead of the client
		// timing out and the handler returning 500.
		http: &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *OpenAIChatClient) Name() string { return c.provider + ":" + c.model }

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model          string              `json:"model"`
	Messages       []openAIChatMessage `json:"messages"`
	MaxTokens      int                 `json:"max_tokens"`
	ResponseFormat map[string]string   `json:"response_format,omitempty"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message openAIChatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends the system + user turn and returns the assistant text.
func (c *OpenAIChatClient) Complete(ctx context.Context, system, user string) (string, error) {
	reqBody := openAIChatRequest{
		Model: c.model,
		Messages: []openAIChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens:      8192, // room for multi-section docs / architecture JSON
		ResponseFormat: map[string]string{"type": "json_object"},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai chat: %w", err)
	}
	defer resp.Body.Close()

	var parsed openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode openai chat: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("openai chat error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("openai chat returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

type openAIChatStreamRequest struct {
	Model     string              `json:"model"`
	Messages  []openAIChatMessage `json:"messages"`
	MaxTokens int                 `json:"max_tokens"`
	Stream    bool                `json:"stream"`
}

type openAIChatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Stream sends the system + user turn with stream:true and invokes onDelta for
// each token fragment as it arrives (Server-Sent Events). It returns the full
// concatenated text. Unlike Complete it does NOT request a JSON object — the
// streamed answer is plain markdown prose.
func (c *OpenAIChatClient) Stream(ctx context.Context, system, user string, onDelta func(string)) (string, error) {
	reqBody := openAIChatStreamRequest{
		Model: c.model,
		Messages: []openAIChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens: 4096,
		Stream:    true,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai chat stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("openai chat stream http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	scanner := bufio.NewScanner(resp.Body)
	// SSE lines (especially with markdown payloads) can be long.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var full strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk openAIChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // ignore keep-alive / non-JSON frames
		}
		if chunk.Error != nil {
			return full.String(), fmt.Errorf("openai chat stream error: %s", chunk.Error.Message)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				full.WriteString(ch.Delta.Content)
				onDelta(ch.Delta.Content)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return full.String(), fmt.Errorf("openai chat stream read: %w", err)
	}
	return full.String(), nil
}
