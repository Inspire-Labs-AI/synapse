package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"project-synapse/backend/internal/rag"
)

// sseWriter wraps a ResponseWriter for Server-Sent Events: it sets the streaming
// headers and flushes after every event so the browser renders tokens live.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &sseWriter{w: w, flusher: flusher}, true
}

// send writes one named SSE event with a JSON data payload and flushes.
func (s *sseWriter) send(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b)
	s.flusher.Flush()
}

// handleQueryStream runs the hybrid RAG pipeline and streams the answer over SSE:
//
//	event: meta   data: { highlighted_files, execution_flow, functions }
//	event: token  data: { delta }            (repeated)
//	event: done   data: {}
func (s *Server) handleQueryStream(w http.ResponseWriter, r *http.Request) {
	if s.orch == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "query layer not configured"})
		return
	}
	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "question is required"})
		return
	}

	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	err := s.orch.QueryStream(
		r.Context(), req.Question, req.Repo,
		func(meta *rag.QueryAnswer) { sse.send("meta", meta) },
		func(delta string) { sse.send("token", map[string]string{"delta": delta}) },
	)
	if err != nil {
		log.Printf("query stream error: %v", err)
		sse.send("error", map[string]string{"error": "query failed"})
		return
	}
	sse.send("done", map[string]string{})
}

// handleDiscoverStream runs capability discovery, sends the structured result,
// then streams a natural-language reuse briefing over SSE:
//
//	event: result data: <BlueprintResponse>
//	event: token  data: { delta }            (repeated narrative)
//	event: done   data: {}
func (s *Server) handleDiscoverStream(w http.ResponseWriter, r *http.Request) {
	if s.blueprint == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "blueprint engine not configured"})
		return
	}
	var req discoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Description) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "description is required"})
		return
	}

	// Run discovery BEFORE switching to SSE so failures return a clean JSON error.
	resp, err := s.blueprint.Discover(r.Context(), req.Description, req.Repo)
	if err != nil {
		log.Printf("blueprint discover error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "discovery failed"})
		return
	}

	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	sse.send("result", resp)

	if err := s.blueprint.StreamNarrative(r.Context(), resp, req.Mode, req.Repo, func(delta string) {
		sse.send("token", map[string]string{"delta": delta})
	}); err != nil {
		log.Printf("blueprint narrate error: %v", err)
		sse.send("error", map[string]string{"error": "narration failed"})
		return
	}
	sse.send("done", map[string]string{})
}
