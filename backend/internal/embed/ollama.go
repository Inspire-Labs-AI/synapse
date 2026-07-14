package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OllamaEmbedder calls a local Ollama instance for embeddings.
//
// The model's native vector dimension MUST match the vector_chunks.embedding
// column. Set SYNAPSE_EMBED_MODEL + SYNAPSE_EMBED_DIM together and recreate the
// column + HNSW index at that dimension (e.g. nomic-embed-text=768,
// mxbai-embed-large / bge-m3 = 1024).
type OllamaEmbedder struct {
	host  string
	model string
	dim   int
	http  *http.Client
}

// NewOllama builds an Ollama embedder. host/model/dim default if empty/zero.
func NewOllama(host, model string, dim int) *OllamaEmbedder {
	if host == "" {
		host = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	if dim <= 0 {
		dim = 768
	}
	return &OllamaEmbedder{
		host:  host,
		model: model,
		dim:   dim,
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Dimensions reports the configured embedding dimension (must match the model).
func (e *OllamaEmbedder) Dimensions() int { return e.dim }
func (e *OllamaEmbedder) Name() string    { return "ollama:" + e.model }

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error"`
}

// Embed calls Ollama once per text (the stable /api/embeddings endpoint takes a
// single prompt).
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := e.embedOne(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// maxEmbedChars is the initial input cap. Token density varies (code with lots
// of punctuation/quotes can tokenize near 1:1), so a char cap alone can't
// guarantee a model's token limit — embedOne retries with progressive
// truncation if the model still rejects the input. The chunk's full source is
// always stored for display; only the embedding input is shortened.
const maxEmbedChars = 1500

func (e *OllamaEmbedder) embedOne(ctx context.Context, text string) ([]float32, error) {
	if runes := []rune(text); len(runes) > maxEmbedChars {
		text = string(runes[:maxEmbedChars])
	}
	for {
		v, err := e.embedRaw(ctx, text)
		if err == nil {
			return v, nil
		}
		runes := []rune(text)
		// Token-dense chunks can exceed a small context window even after the
		// char cap; shrink ~30% and retry until it fits (floor ~300 chars).
		if strings.Contains(err.Error(), "exceeds the context length") && len(runes) > 300 {
			text = string(runes[:len(runes)*7/10])
			continue
		}
		return nil, err
	}
}

func (e *OllamaEmbedder) embedRaw(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(ollamaEmbedRequest{Model: e.model, Prompt: text})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.host+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embeddings: %w", err)
	}
	defer resp.Body.Close()

	var parsed ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("ollama embeddings error: %s", parsed.Error)
	}
	return parsed.Embedding, nil
}
