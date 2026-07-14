package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

// GeminiEmbedder calls Google's Gemini API (gemini-embedding-001) via the
// synchronous batchEmbedContents endpoint, so a whole file's chunks embed in a
// single request — much faster than one-call-per-chunk. Output is truncated to
// `dim` via outputDimensionality and L2-normalized, which Google REQUIRES for
// any dimension other than 3072.
//
// Free-tier rate limits are handled with exponential backoff on HTTP 429 / 5xx.
type GeminiEmbedder struct {
	apiKey    string
	base      string
	model     string
	dim       int
	batchSize int
	backoff   time.Duration // initial retry backoff (doubles each attempt)
	http      *http.Client
}

const (
	geminiDefaultBase  = "https://generativelanguage.googleapis.com/v1beta"
	geminiDefaultModel = "gemini-embedding-001"
	// SEMANTIC_SIMILARITY is symmetric, so the same embedder serves both the
	// ingested documents and the query side of retrieval.
	geminiTaskType = "SEMANTIC_SIMILARITY"
	// Per-request batch cap — modest so a single call stays well under the
	// free-tier per-minute token budget.
	geminiBatchSize = 32
	// Each text is truncated to this many runes (~<=2048 tokens for code); the
	// full source is still stored for display, only the embedding input shrinks.
	maxGeminiChars   = 4000
	geminiMaxRetries = 6
)

// NewGemini builds a Gemini embedder. base/model/dim default if empty/zero.
func NewGemini(apiKey, base, model string, dim int) *GeminiEmbedder {
	if base == "" {
		base = geminiDefaultBase
	}
	if model == "" {
		model = geminiDefaultModel
	}
	if dim <= 0 {
		dim = 768
	}
	return &GeminiEmbedder{
		apiKey:    apiKey,
		base:      base,
		model:     model,
		dim:       dim,
		batchSize: geminiBatchSize,
		backoff:   2 * time.Second,
		http:      &http.Client{Timeout: 120 * time.Second},
	}
}

func (e *GeminiEmbedder) Dimensions() int { return e.dim }
func (e *GeminiEmbedder) Name() string    { return "gemini:" + e.model }

type geminiPart struct {
	Text string `json:"text"`
}
type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}
type geminiEmbedReq struct {
	Model                string        `json:"model"`
	TaskType             string        `json:"taskType,omitempty"`
	Content              geminiContent `json:"content"`
	OutputDimensionality int           `json:"outputDimensionality,omitempty"`
}
type geminiBatchReq struct {
	Requests []geminiEmbedReq `json:"requests"`
}
type geminiBatchResp struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// Embed splits texts into batches and embeds each batch in one request.
func (e *GeminiEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
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

func (e *GeminiEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	modelPath := "models/" + e.model
	reqs := make([]geminiEmbedReq, len(texts))
	for i, t := range texts {
		reqs[i] = geminiEmbedReq{
			Model:                modelPath,
			TaskType:             geminiTaskType,
			Content:              geminiContent{Parts: []geminiPart{{Text: truncateRunes(t, maxGeminiChars)}}},
			OutputDimensionality: e.dim,
		}
	}
	body, err := json.Marshal(geminiBatchReq{Requests: reqs})
	if err != nil {
		return nil, err
	}
	url := e.base + "/models/" + e.model + ":batchEmbedContents"

	var lastErr error
	backoff := e.backoff
	if backoff <= 0 {
		backoff = 2 * time.Second
	}
	for attempt := 0; attempt < geminiMaxRetries; attempt++ {
		parsed, status, retryAfter, derr := e.doRequest(ctx, url, body)
		if derr == nil && parsed != nil && parsed.Error == nil && status < 300 {
			if len(parsed.Embeddings) != len(texts) {
				return nil, fmt.Errorf("gemini returned %d embeddings for %d inputs", len(parsed.Embeddings), len(texts))
			}
			out := make([][]float32, len(texts))
			for i, em := range parsed.Embeddings {
				out[i] = normalize(em.Values)
			}
			return out, nil
		}

		// Rate-limited / transient server error → back off (honouring Retry-After).
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = fmt.Errorf("gemini http %d", status)
			if parsed != nil && parsed.Error != nil {
				lastErr = fmt.Errorf("gemini: %s", parsed.Error.Message)
			}
			wait := backoff
			if retryAfter > 0 {
				wait = retryAfter
			}
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			backoff *= 2
			continue
		}
		// Non-retryable.
		if parsed != nil && parsed.Error != nil {
			return nil, fmt.Errorf("gemini embeddings error: %s", parsed.Error.Message)
		}
		if derr != nil {
			return nil, derr
		}
		return nil, fmt.Errorf("gemini embeddings: unexpected http %d", status)
	}
	return nil, fmt.Errorf("gemini embeddings failed after %d retries: %w", geminiMaxRetries, lastErr)
}

func (e *GeminiEmbedder) doRequest(ctx context.Context, url string, body []byte) (*geminiBatchResp, int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", e.apiKey)

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("gemini embeddings: %w", err)
	}
	defer resp.Body.Close()
	retryAfter := parseRetryAfter(resp.Header)

	var parsed geminiBatchResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		// A non-JSON body on an HTTP error is still reportable via the status.
		if resp.StatusCode >= 400 {
			return &parsed, resp.StatusCode, retryAfter, nil
		}
		return nil, resp.StatusCode, retryAfter, fmt.Errorf("decode gemini response: %w", err)
	}
	return &parsed, resp.StatusCode, retryAfter, nil
}

// normalize L2-normalizes a vector (required for outputDimensionality != 3072).
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1.0 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func truncateRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}
