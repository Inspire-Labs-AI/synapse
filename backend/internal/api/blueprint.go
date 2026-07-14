package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

type discoverRequest struct {
	Description string `json:"description"`
	Repo        string `json:"repo"`           // root_path to scope discovery to ("" = all repos)
	Mode        string `json:"mode,omitempty"` // "validate" | "roadmap" (default roadmap)
}

// handleDiscover runs the AI capability-discovery pipeline: it decomposes a
// feature description into intents and scores them against the codebase into a
// green/yellow/red reuse blueprint.
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
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

	resp, err := s.blueprint.Discover(r.Context(), req.Description, req.Repo)
	if err != nil {
		log.Printf("blueprint discover error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "discovery failed"})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
