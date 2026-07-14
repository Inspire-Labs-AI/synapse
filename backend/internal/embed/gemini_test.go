package embed

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// makeValues returns a deterministic non-normalized vector of length dim.
func makeValues(dim, seed int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32((i+seed)%7) + 1 // never all-zero
	}
	return v
}

func TestGeminiEmbedRequestAndNormalize(t *testing.T) {
	const dim = 768
	var gotURL, gotKey, gotModel, gotTask string
	var gotDim int
	var gotCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		body, _ := io.ReadAll(r.Body)
		var req geminiBatchReq
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request json: %v", err)
		}
		gotCount = len(req.Requests)
		if len(req.Requests) > 0 {
			gotModel = req.Requests[0].Model
			gotTask = req.Requests[0].TaskType
			gotDim = req.Requests[0].OutputDimensionality
		}
		resp := geminiBatchResp{}
		for i := range req.Requests {
			resp.Embeddings = append(resp.Embeddings, struct {
				Values []float32 `json:"values"`
			}{Values: makeValues(dim, i)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewGemini("test-key", srv.URL+"/v1beta", "gemini-embedding-001", dim)
	vecs, err := e.Embed(context.Background(), []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}

	if gotURL != "/v1beta/models/gemini-embedding-001:batchEmbedContents" {
		t.Errorf("URL = %q", gotURL)
	}
	if gotKey != "test-key" {
		t.Errorf("api key header = %q", gotKey)
	}
	if gotModel != "models/gemini-embedding-001" {
		t.Errorf("request model = %q, want models/gemini-embedding-001", gotModel)
	}
	if gotTask != geminiTaskType {
		t.Errorf("taskType = %q", gotTask)
	}
	if gotDim != dim {
		t.Errorf("outputDimensionality = %d, want %d", gotDim, dim)
	}
	if gotCount != 3 {
		t.Errorf("batched request count = %d, want 3", gotCount)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != dim {
			t.Errorf("vec %d dim = %d, want %d", i, len(v), dim)
		}
		// L2-normalized => magnitude ~= 1.
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if mag := math.Sqrt(sum); math.Abs(mag-1.0) > 1e-4 {
			t.Errorf("vec %d not normalized: |v| = %f", i, mag)
		}
	}
	if e.Dimensions() != dim {
		t.Errorf("Dimensions() = %d, want %d", e.Dimensions(), dim)
	}
}

func TestGeminiBatchesOverCap(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		var req geminiBatchReq
		_ = json.Unmarshal(body, &req)
		resp := geminiBatchResp{}
		for range req.Requests {
			resp.Embeddings = append(resp.Embeddings, struct {
				Values []float32 `json:"values"`
			}{Values: makeValues(8, 1)})
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewGemini("k", srv.URL+"/v1beta", "gemini-embedding-001", 8)
	// 70 texts with batchSize 32 => 3 requests (32 + 32 + 6).
	texts := make([]string, 70)
	for i := range texts {
		texts[i] = "t"
	}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(vecs) != 70 {
		t.Errorf("got %d vectors, want 70", len(vecs))
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("made %d requests, want 3 (batch cap %d)", got, geminiBatchSize)
	}
}

func TestGeminiRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"rate limited","status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(geminiBatchResp{Embeddings: []struct {
			Values []float32 `json:"values"`
		}{{Values: makeValues(8, 0)}}})
	}))
	defer srv.Close()

	e := NewGemini("k", srv.URL+"/v1beta", "gemini-embedding-001", 8)
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
