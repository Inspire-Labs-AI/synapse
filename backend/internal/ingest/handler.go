package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Handler serves POST /api/ingest. It shallow-clones a public or private Git
// repository into a local worker directory (injecting a PAT for private/
// enterprise repos when supplied), then runs the AST ingestion pipeline against
// the clone.
//
// Security: the PAT is only ever present in the in-memory clone URL passed
// directly to git via exec args — never logged, never echoed in responses. The
// command runs without a shell, and a "--" separator prevents the URL from
// being parsed as a git option.
type Handler struct {
	Pipeline  *Pipeline
	ClonesDir string        // base directory for shallow clones
	GitBin    string        // git executable (default "git")
	Timeout   time.Duration // clone timeout (default 5m)

	mu  sync.Mutex
	mgr *jobManager
}

func (h *Handler) manager() *jobManager {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mgr == nil {
		h.mgr = newJobManager()
	}
	return h.mgr
}

type ingestRequest struct {
	RepoURL   string `json:"repo_url"`
	PAT       string `json:"pat"`
	LocalPath string `json:"local_path"` // ingest an existing local directory (no clone)
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// ServeHTTP starts an asynchronous ingest and returns a job id immediately; poll
// GET /api/ingest/status?job=<id> for progress. Clone + parse + embed run in a
// background goroutine so large repos never block the request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	repoURL := strings.TrimSpace(req.RepoURL)
	localPath := strings.TrimSpace(req.LocalPath)
	if repoURL == "" && localPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo_url or local_path is required"})
		return
	}
	if h.Pipeline == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ingestion pipeline not configured"})
		return
	}

	// Local-directory ingestion: no clone, parse the path in place.
	if localPath != "" {
		info, statErr := os.Stat(localPath)
		if statErr != nil || !info.IsDir() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "local_path must be an existing directory"})
			return
		}
		abs, absErr := filepath.Abs(localPath)
		if absErr != nil {
			abs = localPath
		}
		job := h.manager().create(filepath.Base(abs))
		go h.runLocalJob(job, abs)
		writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status": "queued"})
		return
	}

	cloneURL, repoName, err := buildCloneURL(repoURL, strings.TrimSpace(req.PAT))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	clonesDir := h.ClonesDir
	if clonesDir == "" {
		clonesDir = ".synapse-clones"
	}
	if err := os.MkdirAll(clonesDir, 0o755); err != nil {
		log.Printf("ingest: prepare clones dir: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not prepare workspace"})
		return
	}

	job := h.manager().create(repoName)
	go h.runJob(job, cloneURL, repoURL, req.PAT, clonesDir, repoName)

	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status": "queued"})
}

// StatusHTTP reports the progress of an ingest job (?job=<id>).
func (h *Handler) StatusHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("job")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job query parameter is required"})
		return
	}
	job := h.manager().get(id)
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	writeJSON(w, http.StatusOK, job.snapshot())
}

// runLocalJob ingests an existing local directory in the background (no clone),
// updating live progress. The absolute path becomes the workspace root_path.
func (h *Handler) runLocalJob(job *Job, absPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	job.setRoot(absPath)
	job.set("ingesting", "parsing + embedding")
	log.Printf("ingest[%s]: local ingest %s", job.ID, absPath)

	if err := h.Pipeline.RunInto(ctx, absPath, &job.stats); err != nil {
		log.Printf("ingest[%s]: local pipeline failed for %s: %v", job.ID, absPath, err)
		job.fail("ingestion failed: " + err.Error())
		return
	}
	job.finish()
	log.Printf("ingest[%s]: local %s complete", job.ID, absPath)
}

// runJob performs the clone + ingest in the background, updating live progress.
func (h *Handler) runJob(job *Job, cloneURL, repoURL, pat, clonesDir, repoName string) {
	gitBin := h.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	cloneTimeout := h.Timeout
	if cloneTimeout <= 0 {
		cloneTimeout = 5 * time.Minute
	}

	// The job outlives the HTTP request; cap the whole run generously.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	targetPath := filepath.Join(clonesDir, slugRe.ReplaceAllString(repoName, "-"))
	_ = os.RemoveAll(targetPath) // re-ingesting the same repo is idempotent

	job.set("cloning", "cloning repository")
	log.Printf("ingest[%s]: shallow-cloning %s -> %s", job.ID, redactURL(repoURL), targetPath)
	cloneCtx, cloneCancel := context.WithTimeout(ctx, cloneTimeout)
	cmd := exec.CommandContext(cloneCtx, gitBin, "clone", "--depth", "1", "--", cloneURL, targetPath)
	out, cloneErr := cmd.CombinedOutput()
	cloneCancel()
	if cloneErr != nil {
		log.Printf("ingest[%s]: clone failed for %s: %v", job.ID, redactURL(repoURL), cloneErr)
		job.fail("git clone failed: " + sanitizeGitOutput(string(out), pat))
		return
	}

	absRoot, _ := filepath.Abs(targetPath)
	job.setRoot(absRoot)
	job.set("ingesting", "parsing + embedding")

	if err := h.Pipeline.RunInto(ctx, targetPath, &job.stats); err != nil {
		log.Printf("ingest[%s]: pipeline failed for %s: %v", job.ID, repoName, err)
		job.fail("ingestion failed: " + err.Error())
		return
	}
	job.finish()
	log.Printf("ingest[%s]: %s complete", job.ID, repoName)
}

// buildCloneURL validates the repo URL and, when a PAT is supplied, rewrites it
// to inject the token as userinfo: https://<pat>@host/path.git.
func buildCloneURL(repoURL, pat string) (cloneURL, repoName string, err error) {
	u, parseErr := url.Parse(repoURL)
	if parseErr != nil {
		return "", "", fmt.Errorf("invalid repo_url")
	}
	if u.Scheme != "https" || u.Host == "" {
		return "", "", fmt.Errorf("repo_url must be an https:// URL")
	}
	repoName = repoNameFromPath(u.Path)
	if pat != "" {
		// url.User encodes the token safely into the authority.
		u.User = url.User(pat)
	}
	return u.String(), repoName, nil
}

// repoNameFromPath derives "owner-repo" from a clone path.
func repoNameFromPath(p string) string {
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	parts := strings.Split(p, "/")
	switch {
	case len(parts) >= 2:
		return parts[len(parts)-2] + "-" + parts[len(parts)-1]
	case len(parts) == 1 && parts[0] != "":
		return parts[0]
	default:
		return "repo"
	}
}

// redactURL masks any userinfo so a token embedded directly in the URL is never
// written to logs.
func redactURL(repoURL string) string {
	if u, err := url.Parse(repoURL); err == nil && u.User != nil {
		u.User = url.User("***")
		return u.String()
	}
	return repoURL
}

// sanitizeGitOutput trims git's stderr and scrubs the PAT from it before it is
// surfaced to the client.
func sanitizeGitOutput(out, pat string) string {
	out = strings.TrimSpace(out)
	if pat != "" {
		out = strings.ReplaceAll(out, pat, "***")
	}
	if len(out) > 500 {
		out = out[:500] + "…"
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
