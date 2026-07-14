package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// VoyageEmbedder calls Voyage AI's embeddings API with a code-specialized model
// (voyage-code-3, a top code-retrieval model). Voyage's API mirrors OpenAI's but
// adds output_dimension; voyage-code-3 supports 256/512/1024(default)/2048 — NOT
// 768 — so using it requires SYNAPSE_EMBED_DIM ∈ {256,512,1024,2048} and a
// matching vector_chunks column. Inputs are batched and transient failures
// retried with backoff.
type VoyageEmbedder struct {
	apiKey    string
	base      string
	model     string
	dim       int
	batchSize int
	backoff   time.Duration
	http      *http.Client
}

const (
	voyageDefaultBase  = "https://api.voyageai.com/v1"
	voyageDefaultModel = "voyage-code-3"
	voyageBatchSize  = 128
	maxVoyageChars   = 24000 // voyage-code-3 has a large context window
	voyageMaxRetries = 8
	voyageMaxBackoff = 30 * time.Second
)

// NewVoyage builds a Voyage code embedder. base/model default if empty; dim
// defaults to 1024 (voyage-code-3's default, since 768 is unsupported).
func NewVoyage(apiKey, base, model string, dim int) *VoyageEmbedder {
	if base == "" {
		base = voyageDefaultBase
	}
	if model == "" {
		model = voyageDefaultModel
	}
	if dim <= 0 {
		dim = 1024
	}
	return &VoyageEmbedder{
		apiKey: apiKey, base: base, model: model, dim: dim,
		batchSize: voyageBatchSize, backoff: time.Second,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *VoyageEmbedder) Dimensions() int { return e.dim }
func (e *VoyageEmbedder) Name() string    { return "voyage:" + e.model }

type voyageRequest struct {
	Model           string   `json:"model"`
	Input           []string `json:"input"`
	OutputDimension int      `json:"output_dimension,omitempty"`
}

type voyageResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Detail string `json:"detail"` // error message field on failures
}

// Embed splits texts into batches and embeds each in one request.
func (e *VoyageEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := start + e.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *VoyageEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	inputs := make([]string, len(texts))
	for i, t := range texts {
		inputs[i] = truncateRunes(t, maxVoyageChars)
	}
	body, err := json.Marshal(voyageRequest{Model: e.model, Input: inputs, OutputDimension: e.dim})
	if err != nil {
		return nil, err
	}
	url := e.base + "/embeddings"

	var lastErr error
	backoff := e.backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for attempt := 0; attempt < voyageMaxRetries; attempt++ {
		parsed, status, retryAfter, derr := e.doRequest(ctx, url, body)
		if derr == nil && parsed != nil && status < 300 && len(parsed.Data) == len(texts) {
			out := make([][]float32, len(texts))
			for _, d := range parsed.Data {
				if d.Index < 0 || d.Index >= len(out) {
					return nil, fmt.Errorf("voyage embedding index %d out of range", d.Index)
				}
				out[d.Index] = d.Embedding
			}
			return out, nil
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = fmt.Errorf("voyage http %d", status)
			wait := backoff
			if retryAfter > 0 {
				wait = retryAfter
			}
			if wait > voyageMaxBackoff {
				wait = voyageMaxBackoff
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			backoff *= 2
			if backoff > voyageMaxBackoff {
				backoff = voyageMaxBackoff
			}
			continue
		}
		if parsed != nil && parsed.Detail != "" {
			return nil, fmt.Errorf("voyage embeddings error: %s", parsed.Detail)
		}
		if derr != nil {
			return nil, derr
		}
		return nil, fmt.Errorf("voyage embeddings: unexpected http %d", status)
	}
	return nil, fmt.Errorf("voyage embeddings failed after %d retries: %w", voyageMaxRetries, lastErr)
}

func (e *VoyageEmbedder) doRequest(ctx context.Context, url string, body []byte) (*voyageResponse, int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("voyage embeddings: %w", err)
	}
	defer resp.Body.Close()
	retryAfter := parseRetryAfter(resp.Header)

	var parsed voyageResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		if resp.StatusCode >= 400 {
			return &parsed, resp.StatusCode, retryAfter, nil
		}
		return nil, resp.StatusCode, retryAfter, fmt.Errorf("decode voyage response: %w", err)
	}
	return &parsed, resp.StatusCode, retryAfter, nil
}
