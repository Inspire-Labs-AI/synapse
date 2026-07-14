package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

type queryRequest struct {
	Question string `json:"question"`
	Repo     string `json:"repo"` // root_path to scope the search to ("" = all repos)
}

// handleQuery runs the hybrid RAG pipeline and returns the structured answer
// contract { answer, highlighted_files, execution_flow }.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
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

	answer, err := s.orch.Query(r.Context(), req.Question, req.Repo)
	if err != nil {
		log.Printf("query error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	writeJSON(w, http.StatusOK, answer)
}
