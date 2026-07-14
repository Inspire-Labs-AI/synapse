// Package llm wraps chat-completion providers behind a single interface so the
// RAG orchestrator can swap between Claude (Anthropic), OpenAI, or an offline
// deterministic responder without changing call sites.
package llm

import (
	"context"
	"strings"
)

// ChatClient produces a single completion from a system prompt + user message.
type ChatClient interface {
	Complete(ctx context.Context, system, user string) (string, error)
	Name() string
}

// StreamingChatClient is an optional capability: a ChatClient that can emit its
// completion incrementally, invoking onDelta for each text fragment and
// returning the full text. Callers type-assert for it and fall back to Complete
// when a provider does not implement it.
type StreamingChatClient interface {
	Stream(ctx context.Context, system, user string, onDelta func(string)) (string, error)
}

// CleanMarkdown repairs over-escaped sequences some models emit inside JSON
// string values — a literal backslash-n / backslash-t (and \r\n) instead of a
// real newline/tab. Without this, markdown renders with visible "\n" and headings
// like "## Foo" never start a line, so they show as plain text. Correctly-parsed
// content has no literal "\n", so this is a safe no-op there.
func CleanMarkdown(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	s = strings.ReplaceAll(s, `\r\n`, "\n")
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\t`, "\t")
	return s
}

// Config selects and configures the chat provider.
type Config struct {
	Provider       string // auto | template | anthropic | openai | openrouter | ollama
	Model          string
	AnthropicKey   string
	OpenAIKey      string
	OpenAIBase     string
	OpenRouterKey  string
	OpenRouterBase string
	OllamaHost     string
}

// Per-provider defaults applied when Config.Model is empty. OpenRouter and Ollama
// are OpenAI-compatible, so they reuse the OpenAI chat client with their own base
// URL and a provider-appropriate default model.
const (
	defaultOpenRouterBase  = "https://openrouter.ai/api/v1"
	defaultOpenRouterModel = "openai/gpt-4o-mini"
	defaultOllamaBase      = "http://localhost:11434"
	defaultOllamaModel     = "qwen2.5-coder:3b"
)

// NewChatClient builds a ChatClient from cfg. The "auto" provider walks a
// preference chain — the first present API key wins, falling back to local
// Ollama — so dropping in an ANTHROPIC_API_KEY (or OPENAI/OPENROUTER) switches
// providers with no other changes:
//
//	anthropic key -> openai key -> openrouter key -> ollama (local)
//
// It returns (nil, nil) only for the explicit "template"/"none" provider, which
// signals the caller to use the offline deterministic responder.
func NewChatClient(cfg Config) (ChatClient, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" || provider == "auto" {
		switch {
		case cfg.AnthropicKey != "":
			provider = "anthropic"
		case cfg.OpenAIKey != "":
			provider = "openai"
		case cfg.OpenRouterKey != "":
			provider = "openrouter"
		default:
			provider = "ollama"
		}
	}

	switch provider {
	case "template", "none", "offline":
		return nil, nil
	case "anthropic", "claude":
		return NewAnthropic(cfg.AnthropicKey, cfg.Model), nil
	case "openai", "gpt":
		return NewOpenAIChat(cfg.OpenAIKey, cfg.OpenAIBase, cfg.Model, "openai"), nil
	case "openrouter":
		base := cfg.OpenRouterBase
		if base == "" {
			base = defaultOpenRouterBase
		}
		model := cfg.Model
		if model == "" {
			model = defaultOpenRouterModel
		}
		return NewOpenAIChat(cfg.OpenRouterKey, base, model, "openrouter"), nil
	case "ollama":
		base := strings.TrimRight(cfg.OllamaHost, "/")
		if base == "" {
			base = defaultOllamaBase
		}
		model := cfg.Model
		if model == "" {
			model = defaultOllamaModel
		}
		// Ollama ignores the auth header, but the OpenAI client always sends a
		// Bearer token — pass a non-empty placeholder.
		return NewOpenAIChat("ollama", base+"/v1", model, "ollama"), nil
	default:
		return nil, nil
	}
}
