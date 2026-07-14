package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenAIStreamParsesSSE verifies the streaming client: it sends stream:true
// (and NOT response_format json_object), parses each SSE delta, invokes onDelta
// in order, and returns the concatenated text.
func TestOpenAIStreamParsesSSE(t *testing.T) {
	deltas := []string{"Hello", ", ", "world", "!"}

	var gotStream bool
	var gotResponseFormat bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if s, ok := req["stream"].(bool); ok && s {
			gotStream = true
		}
		if _, ok := req["response_format"]; ok {
			gotResponseFormat = true
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, d := range deltas {
			frame := map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": d}}}}
			b, _ := json.Marshal(frame)
			_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := NewOpenAIChat("test-key", srv.URL, "test-model", "openrouter")

	var got []string
	full, err := c.Stream(context.Background(), "sys", "user", func(delta string) {
		got = append(got, delta)
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if !gotStream {
		t.Errorf("request did not set stream:true")
	}
	if gotResponseFormat {
		t.Errorf("streaming request must NOT request a json_object response_format")
	}
	if full != "Hello, world!" {
		t.Errorf("full = %q, want %q", full, "Hello, world!")
	}
	if strings.Join(got, "") != "Hello, world!" {
		t.Errorf("deltas joined = %q, want %q", strings.Join(got, ""), "Hello, world!")
	}
	if len(got) != len(deltas) {
		t.Errorf("got %d deltas, want %d (token-by-token)", len(got), len(deltas))
	}

	// The client must also satisfy the optional StreamingChatClient interface.
	var _ StreamingChatClient = c
}

func TestOpenAIStreamSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	c := NewOpenAIChat("k", srv.URL, "m", "openrouter")
	_, err := c.Stream(context.Background(), "s", "u", func(string) {})
	if err == nil {
		t.Fatal("expected an error on HTTP 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention status 429: %v", err)
	}
}
