// Package embed provides the embedding-client abstraction for Project Synapse.
//
// Anthropic does not offer an embeddings endpoint, so embeddings come from
// OpenAI (text-embedding-3-small), a local Ollama instance (nomic-embed-text),
// or a built-in deterministic local embedder. The deterministic embedder is a
// feature-hashing bag-of-tokens model: it needs no network or API key, yet
// cosine similarity over its vectors reflects real lexical/semantic token
// overlap — so the full RAG pipeline can be run and verified offline, with a
// real provider plugged in via the same interface for production.
package embed

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultDim matches the vector_chunks.embedding column (VECTOR(1536)) and the
// OpenAI text-embedding-3-small dimension.
const DefaultDim = 1536

// Embedder turns text into fixed-dimension vectors.
type Embedder interface {
	// Embed returns one vector per input string, in order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimensions is the vector length this embedder produces.
	Dimensions() int
	// Name identifies the backend (for logging).
	Name() string
}

// Config selects and configures the embedding backend.
type Config struct {
	Provider       string // auto | deterministic | jina | voyage | openai | openrouter | gemini | ollama
	Model          string
	Dim            int
	JinaKey        string
	JinaBase       string
	VoyageKey      string
	VoyageBase     string
	OpenAIKey      string
	OpenAIBase     string
	OpenRouterKey  string
	OpenRouterBase string
	GeminiKey      string
	GeminiBase     string
	OllamaHost     string
}

// New builds an Embedder from cfg. In "auto" mode it walks a preference chain —
// OpenAI → OpenRouter → Gemini → local Ollama (if reachable) → deterministic —
// selecting the first available backend, so dropping in a key switches providers
// with no other config change.
func New(cfg Config) (Embedder, error) {
	dim := cfg.Dim
	if dim <= 0 {
		dim = DefaultDim
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" || provider == "auto" {
		provider = pickAuto(cfg)
	}

	switch provider {
	case "deterministic", "local", "hash":
		return NewDeterministic(dim), nil
	case "jina":
		if cfg.JinaKey == "" {
			return nil, fmt.Errorf("jina embedder selected but JINA_API_KEY is empty")
		}
		return NewJina(cfg.JinaKey, cfg.JinaBase, cfg.Model, dim), nil
	case "voyage":
		if cfg.VoyageKey == "" {
			return nil, fmt.Errorf("voyage embedder selected but VOYAGE_API_KEY is empty")
		}
		return NewVoyage(cfg.VoyageKey, cfg.VoyageBase, cfg.Model, dim), nil
	case "openai":
		if cfg.OpenAIKey == "" {
			return nil, fmt.Errorf("openai embedder selected but OPENAI_API_KEY is empty")
		}
		return NewOpenAI(cfg.OpenAIKey, cfg.OpenAIBase, cfg.Model, dim), nil
	case "openrouter":
		if cfg.OpenRouterKey == "" {
			return nil, fmt.Errorf("openrouter embedder selected but OPENROUTER_API_KEY is empty")
		}
		return NewOpenRouter(cfg.OpenRouterKey, cfg.OpenRouterBase, cfg.Model, dim), nil
	case "gemini", "google":
		if cfg.GeminiKey == "" {
			return nil, fmt.Errorf("gemini embedder selected but GEMINI_API_KEY is empty")
		}
		return NewGemini(cfg.GeminiKey, cfg.GeminiBase, cfg.Model, dim), nil
	case "ollama":
		return NewOllama(cfg.OllamaHost, cfg.Model, dim), nil
	default:
		return nil, fmt.Errorf("unknown embed provider %q", cfg.Provider)
	}
}

// pickAuto resolves the "auto" provider preference chain: a hosted key wins in
// priority order, then a reachable local Ollama, then the always-available
// deterministic embedder as the offline floor.
func pickAuto(cfg Config) string {
	switch {
	case cfg.JinaKey != "": // code-specialized + 768-native → preferred for code
		return "jina"
	case cfg.OpenAIKey != "":
		return "openai"
	case cfg.OpenRouterKey != "":
		return "openrouter"
	case cfg.GeminiKey != "":
		return "gemini"
	case ollamaReachable(cfg.OllamaHost):
		return "ollama"
	default:
		return "deterministic"
	}
	// Voyage is NOT in the auto chain: voyage-code-3 can't produce 768 dims, so it
	// needs an explicit provider + a matching SYNAPSE_EMBED_DIM/column.
}

// ollamaReachable probes a local Ollama instance with a short timeout so "auto"
// only selects it when the daemon is actually running. Called only when no
// hosted key is configured, so it never delays the common path.
func ollamaReachable(host string) bool {
	if host == "" {
		host = "http://localhost:11434"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(host, "/")+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 500
}

// parseRetryAfter reads a 429/503 "Retry-After" header (delta-seconds or an HTTP
// date) into a wait duration, capped so a misbehaving server can't stall
// ingestion. Returns 0 when absent/unparseable (callers fall back to backoff).
func parseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	const capWait = 60 * time.Second
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		if d := time.Duration(secs) * time.Second; d <= capWait {
			return d
		}
		return capWait
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			if d > capWait {
				return capWait
			}
			return d
		}
	}
	return 0
}

// ToVectorLiteral formats a vector as a pgvector text literal: "[0.1,0.2,...]".
// Bound as a string and cast with $n::vector in SQL.
func ToVectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', 6, 32))
	}
	b.WriteByte(']')
	return b.String()
}
