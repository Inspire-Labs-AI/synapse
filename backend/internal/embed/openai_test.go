package embed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// openAIResponse builds an embeddings response for the given inputs, optionally
// returning the data rows shuffled to exercise index-based placement.
func openAIResponse(n, dim int, shuffle bool) openAIEmbedResponse {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	if shuffle && n > 1 {
		idx[0], idx[n-1] = idx[n-1], idx[0] // swap first/last so order != index
	}
	resp := openAIEmbedResponse{}
	for _, i := range idx {
		resp.Data = append(resp.Data, struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{Index: i, Embedding: makeValues(dim, i)})
	}
	return resp
}

func TestOpenAIEmbedRequestAndDimensions(t *testing.T) {
	const dim = 768
	var gotURL, gotAuth, gotModel string
	var gotDim, gotCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req openAIEmbedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request json: %v", err)
		}
		gotModel = req.Model
		gotDim = req.Dimensions
		gotCount = len(req.Input)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openAIResponse(len(req.Input), dim, true))
	}))
	defer srv.Close()

	e := NewOpenAI("test-key", srv.URL, "text-embedding-3-small", dim)
	vecs, err := e.Embed(context.Background(), []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}

	if gotURL != "/embeddings" {
		t.Errorf("URL = %q, want /embeddings", gotURL)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotModel != "text-embedding-3-small" {
		t.Errorf("model = %q", gotModel)
	}
	if gotDim != dim {
		t.Errorf("dimensions = %d, want %d (3-small honours the param)", gotDim, dim)
	}
	if gotCount != 3 {
		t.Errorf("input count = %d, want 3", gotCount)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != dim {
			t.Errorf("vec %d dim = %d, want %d", i, len(v), dim)
		}
		// Placed by Index: vec[i] must equal makeValues(dim, i) even though the
		// server returned the rows shuffled.
		if v[0] != makeValues(dim, i)[0] {
			t.Errorf("vec %d not placed by index", i)
		}
	}
	if e.Dimensions() != dim {
		t.Errorf("Dimensions() = %d, want %d", e.Dimensions(), dim)
	}
}

// ada-002 does not support the dimensions parameter, so it must be omitted.
func TestOpenAIOmitsDimensionsForAda(t *testing.T) {
	var sentDim int
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &raw)
		var req openAIEmbedRequest
		_ = json.Unmarshal(body, &req)
		sentDim = req.Dimensions
		_ = json.NewEncoder(w).Encode(openAIResponse(len(req.Input), DefaultDim, false))
	}))
	defer srv.Close()

	e := NewOpenAI("k", srv.URL, "text-embedding-ada-002", 768)
	if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if sentDim != 0 {
		t.Errorf("dimensions = %d, want 0 (omitted for ada-002)", sentDim)
	}
	if _, present := raw["dimensions"]; present {
		t.Errorf("dimensions key should be absent from the ada-002 request body")
	}
}

// The factory must route provider "openai" to an OpenAI embedder carrying the
// configured dim — the exact path cmd/server uses.
func TestNewSelectsOpenAIWithDim(t *testing.T) {
	e, err := New(Config{Provider: "openai", Model: "text-embedding-3-small", Dim: 768, OpenAIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.Name() != "openai:text-embedding-3-small" {
		t.Errorf("Name() = %q, want openai:text-embedding-3-small", e.Name())
	}
	if e.Dimensions() != 768 {
		t.Errorf("Dimensions() = %d, want 768", e.Dimensions())
	}
	if _, err := New(Config{Provider: "openai", Dim: 768}); err == nil {
		t.Errorf("expected error when OPENAI_API_KEY is empty")
	}
}

// The OpenRouter embedder must still send the dimensions param for the
// namespaced "openai/text-embedding-3-*" model id (so it fits the 768 column).
func TestOpenRouterSendsDimensions(t *testing.T) {
	var gotAuth, gotModel string
	var gotDim int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req openAIEmbedRequest
		_ = json.Unmarshal(body, &req)
		gotModel = req.Model
		gotDim = req.Dimensions
		_ = json.NewEncoder(w).Encode(openAIResponse(len(req.Input), 768, false))
	}))
	defer srv.Close()

	e := NewOpenRouter("or-key", srv.URL, "", 768) // empty model -> default
	if e.Name() != "openrouter:openai/text-embedding-3-small" {
		t.Errorf("Name() = %q", e.Name())
	}
	if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if gotAuth != "Bearer or-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotModel != "openai/text-embedding-3-small" {
		t.Errorf("model = %q", gotModel)
	}
	if gotDim != 768 {
		t.Errorf("dimensions = %d, want 768 (namespaced 3-* id must still send it)", gotDim)
	}
}

// The "auto" chain must select providers in priority order: OpenAI -> OpenRouter
// -> Gemini -> Ollama (if reachable) -> deterministic.
func TestAutoProviderChain(t *testing.T) {
	mk := func(c Config) Embedder {
		c.Provider = "auto"
		c.Dim = 768
		e, err := New(c)
		if err != nil {
			t.Fatalf("New(auto): %v", err)
		}
		return e
	}

	if got := mk(Config{OpenAIKey: "k", OpenRouterKey: "k", GeminiKey: "k"}).Name(); got != "openai:text-embedding-3-small" {
		t.Errorf("openai should win: %q", got)
	}
	if got := mk(Config{OpenRouterKey: "k", GeminiKey: "k"}).Name(); got != "openrouter:openai/text-embedding-3-small" {
		t.Errorf("openrouter should be next: %q", got)
	}
	if got := mk(Config{GeminiKey: "k"}).Name(); got != "gemini:gemini-embedding-001" {
		t.Errorf("gemini should be next: %q", got)
	}

	// Reachable Ollama (mock /api/tags) with no hosted key -> ollama.
	ol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ol.Close()
	if got := mk(Config{OllamaHost: ol.URL}).Name(); got != "ollama:nomic-embed-text" {
		t.Errorf("reachable ollama should be selected: %q", got)
	}

	// No keys, Ollama unreachable -> deterministic floor.
	if got := mk(Config{OllamaHost: "http://127.0.0.1:1"}).Name(); got != "deterministic" {
		t.Errorf("offline floor should be deterministic: %q", got)
	}
}

// Code-specialized providers route correctly, and auto prefers Jina (code +
// 768-native) when its key is present.
func TestCodeEmbeddingProviders(t *testing.T) {
	j, err := New(Config{Provider: "jina", JinaKey: "k", Dim: 768})
	if err != nil || j.Name() != "jina:jina-embeddings-v2-base-code" || j.Dimensions() != 768 {
		t.Errorf("jina routing: name=%q dim=%d err=%v", safeName(j), safeDim(j), err)
	}
	v, err := New(Config{Provider: "voyage", VoyageKey: "k", Dim: 1024})
	if err != nil || v.Name() != "voyage:voyage-code-3" || v.Dimensions() != 1024 {
		t.Errorf("voyage routing: name=%q dim=%d err=%v", safeName(v), safeDim(v), err)
	}
	a, err := New(Config{Provider: "auto", JinaKey: "k", OpenAIKey: "k", Dim: 768})
	if err != nil || a.Name() != "jina:jina-embeddings-v2-base-code" {
		t.Errorf("auto should prefer jina: name=%q err=%v", safeName(a), err)
	}
	if _, err := New(Config{Provider: "jina", Dim: 768}); err == nil {
		t.Errorf("expected error when JINA_API_KEY is empty")
	}
}

func safeName(e Embedder) string {
	if e == nil {
		return "<nil>"
	}
	return e.Name()
}
func safeDim(e Embedder) int {
	if e == nil {
		return -1
	}
	return e.Dimensions()
}

func TestOpenAIBatchesOverCap(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		var req openAIEmbedRequest
		_ = json.Unmarshal(body, &req)
		_ = json.NewEncoder(w).Encode(openAIResponse(len(req.Input), 8, false))
	}))
	defer srv.Close()

	e := NewOpenAI("k", srv.URL, "text-embedding-3-small", 8)
	// 300 texts with batchSize 128 => 3 requests (128 + 128 + 44).
	texts := make([]string, 300)
	for i := range texts {
		texts[i] = "t"
	}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(vecs) != 300 {
		t.Errorf("got %d vectors, want 300", len(vecs))
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("made %d requests, want 3 (batch cap %d)", got, openAIBatchSize)
	}
}

// A 429 with a Retry-After header is obeyed (waits ~the header, not the large
// exponential backoff), and the error label names the real provider (jina).
func TestRetryAfterAndProviderLabel(t *testing.T) {
	// Honour Retry-After: first 429 with Retry-After:1, then success. With a huge
	// base backoff, finishing in ~1s proves the header (not the backoff) was used.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req openAIEmbedRequest
		_ = json.Unmarshal(body, &req)
		_ = json.NewEncoder(w).Encode(openAIResponse(len(req.Input), 8, false))
	}))
	defer srv.Close()

	e := NewOpenAI("k", srv.URL, "text-embedding-3-small", 8)
	e.backoff = 20 * time.Second // would dominate if Retry-After were ignored
	start := time.Now()
	if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed should recover: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("waited %v — Retry-After (1s) was not honoured", d)
	}

	// Provider label: a Jina embedder hitting persistent 429 must say "jina".
	jsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer jsrv.Close()
	j := NewJina("k", jsrv.URL, "", 768)
	j.backoff = time.Millisecond // keep the 8 retries fast
	_, err := j.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "jina") {
		t.Errorf("error should name the jina provider, got: %v", err)
	}
}

func TestOpenAIRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req openAIEmbedRequest
		_ = json.Unmarshal(body, &req)
		_ = json.NewEncoder(w).Encode(openAIResponse(len(req.Input), 8, false))
	}))
	defer srv.Close()

	e := NewOpenAI("k", srv.URL, "text-embedding-3-small", 8)
	e.backoff = time.Millisecond // shrink retry backoff so the test is fast
	vecs, err := e.Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("Embed should recover after 429: %v", err)
	}
	if len(vecs) != 1 {
		t.Errorf("got %d vectors, want 1", len(vecs))
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Errorf("expected a retry after 429, calls = %d", got)
	}
}
