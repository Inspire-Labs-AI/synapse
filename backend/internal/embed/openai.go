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

// OpenAIEmbedder calls the OpenAI embeddings API (text-embedding-3-small by
// default). For the text-embedding-3 family it requests `dim` via the
// `dimensions` parameter (Matryoshka truncation), so the output matches the
// vector_chunks column without a schema change. Inputs are batched per request
// and transient failures (HTTP 429 / 5xx) retried with exponential backoff, so
// a high worker-pool concurrency stays fast and reliable. Raw HTTP keeps the
// dependency surface minimal.
type OpenAIEmbedder struct {
	apiKey    string
	base      string
	model     string
	dim       int
	label     string // provider label for Name()/errors — "openai" | "openrouter" | "jina"
	batchSize int
	backoff   time.Duration // initial retry backoff (doubles each attempt)
	http      *http.Client
}

const (
	openAIDefaultBase  = "https://api.openai.com/v1"
	openAIDefaultModel = "text-embedding-3-small"
	// Inputs per request — well under OpenAI's 2048-input cap, small enough to
	// stay inside the per-request token budget while still embedding many chunks
	// in one round-trip.
	openAIBatchSize = 128
	// Each input is truncated to this many runes. OpenAI's per-input limit is
	// ~8191 tokens; ~16k chars of code is ~4k tokens, safely under it while
	// keeping whole ~200-line chunks intact. The full source is still stored for
	// display. Code-specialized providers override this with a larger cap.
	maxOpenAIChars = 16000
	// Generous retries for rate limits (free tiers like Jina 429 under burst);
	// each wait honours the server's Retry-After, capped at openAIMaxBackoff.
	openAIMaxRetries = 8
	openAIMaxBackoff = 30 * time.Second
)

// NewOpenAI builds an OpenAI embedder. base/model/dim default if empty/zero.
func NewOpenAI(apiKey, base, model string, dim int) *OpenAIEmbedder {
	if base == "" {
		base = openAIDefaultBase
	}
	if model == "" {
		model = openAIDefaultModel
	}
	if dim <= 0 {
		dim = DefaultDim
	}
	return &OpenAIEmbedder{
		apiKey:    apiKey,
		base:      base,
		model:     model,
		dim:       dim,
		label:     "openai",
		batchSize: openAIBatchSize,
		backoff:   time.Second,
		http:      &http.Client{Timeout: 60 * time.Second},
	}
}

// NewOpenRouter builds an OpenAI-compatible embedder pointed at OpenRouter's
// embeddings endpoint. OpenRouter mirrors the OpenAI API, so the same client
// works; only the base URL, default model, and label differ.
func NewOpenRouter(apiKey, base, model string, dim int) *OpenAIEmbedder {
	if base == "" {
		base = "https://openrouter.ai/api/v1"
	}
	if model == "" {
		// OpenRouter ids are namespaced; this maps onto OpenAI's 3-small, which
		// honours the dimensions param (so it fits the 768-dim column).
		model = "openai/text-embedding-3-small"
	}
	e := NewOpenAI(apiKey, base, model, dim)
	e.label = "openrouter"
	return e
}

// NewJina builds a CODE-SPECIALIZED embedder using Jina's OpenAI-compatible
// /embeddings endpoint. jina-embeddings-v2-base-code is trained on code and is
// natively 768-dim, so it drops into the existing column with no schema change
// — the recommended quality upgrade for this code-intelligence product.
func NewJina(apiKey, base, model string, dim int) *OpenAIEmbedder {
	if base == "" {
		base = "https://api.jina.ai/v1"
	}
	if model == "" {
		model = "jina-embeddings-v2-base-code"
	}
	e := NewOpenAI(apiKey, base, model, dim)
	e.label = "jina"
	return e
}

func (e *OpenAIEmbedder) Dimensions() int { return e.dim }
func (e *OpenAIEmbedder) Name() string    { return e.label + ":" + e.model }

// supportsDimensions reports whether the model honours the `dimensions` request
// parameter. The text-embedding-3 family does (including OpenRouter's namespaced
// "openai/text-embedding-3-*" ids); the legacy ada-002 does not.
func (e *OpenAIEmbedder) supportsDimensions() bool {
	return strings.Contains(e.model, "text-embedding-3")
}

type openAIEmbedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed splits texts into batches and embeds each batch in one request.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
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

func (e *OpenAIEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	inputs := make([]string, len(texts))
	for i, t := range texts {
		inputs[i] = truncateRunes(t, maxOpenAIChars)
	}
	reqBody := openAIEmbedRequest{Model: e.model, Input: inputs}
	if e.supportsDimensions() {
		reqBody.Dimensions = e.dim
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	url := e.base + "/embeddings"

	var lastErr error
	backoff := e.backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for attempt := 0; attempt < openAIMaxRetries; attempt++ {
		parsed, status, retryAfter, derr := e.doRequest(ctx, url, body)
		if derr == nil && parsed != nil && parsed.Error == nil && status < 300 {
			if len(parsed.Data) != len(texts) {
				return nil, fmt.Errorf("%s returned %d embeddings for %d inputs", e.label, len(parsed.Data), len(texts))
			}
			// `data` is index-ordered by the API; place by Index defensively.
			out := make([][]float32, len(texts))
			for _, d := range parsed.Data {
				if d.Index < 0 || d.Index >= len(out) {
					return nil, fmt.Errorf("%s embedding index %d out of range (%d inputs)", e.label, d.Index, len(texts))
				}
				out[d.Index] = d.Embedding
			}
			return out, nil
		}

		// Rate-limited / transient server error → back off (honouring Retry-After).
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = fmt.Errorf("%s http %d", e.label, status)
			if parsed != nil && parsed.Error != nil {
				lastErr = fmt.Errorf("%s: %s", e.label, parsed.Error.Message)
			}
			wait := backoff
			if retryAfter > 0 {
				wait = retryAfter
			}
			if wait > openAIMaxBackoff {
				wait = openAIMaxBackoff
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			backoff *= 2
			if backoff > openAIMaxBackoff {
				backoff = openAIMaxBackoff
			}
			continue
		}
		// Non-retryable.
		if parsed != nil && parsed.Error != nil {
			return nil, fmt.Errorf("%s embeddings error: %s", e.label, parsed.Error.Message)
		}
		if derr != nil {
			return nil, derr
		}
		return nil, fmt.Errorf("%s embeddings: unexpected http %d", e.label, status)
	}
	return nil, fmt.Errorf("%s embeddings failed after %d retries: %w", e.label, openAIMaxRetries, lastErr)
}

func (e *OpenAIEmbedder) doRequest(ctx context.Context, url string, body []byte) (*openAIEmbedResponse, int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s embeddings: %w", e.label, err)
	}
	defer resp.Body.Close()
	retryAfter := parseRetryAfter(resp.Header)

	var parsed openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		// A non-JSON body on an HTTP error is still reportable via the status.
		if resp.StatusCode >= 400 {
			return &parsed, resp.StatusCode, retryAfter, nil
		}
		return nil, resp.StatusCode, retryAfter, fmt.Errorf("decode %s response: %w", e.label, err)
	}
	return &parsed, resp.StatusCode, retryAfter, nil
}
