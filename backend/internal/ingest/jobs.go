package ingest

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
)

// Job tracks one asynchronous ingestion run: a status/phase string plus the live
// atomic progress counters the pipeline writes into.
type Job struct {
	ID    string
	Repo  string
	stats Stats

	mu       sync.Mutex
	status   string // queued | cloning | ingesting | done | error
	phase    string
	rootPath string
	errMsg   string
}

func (j *Job) set(status, phase string) {
	j.mu.Lock()
	j.status, j.phase = status, phase
	j.mu.Unlock()
}

func (j *Job) setRoot(p string) {
	j.mu.Lock()
	j.rootPath = p
	j.mu.Unlock()
}

func (j *Job) fail(msg string) {
	j.mu.Lock()
	j.status, j.phase, j.errMsg = "error", "failed", msg
	j.mu.Unlock()
}

func (j *Job) finish() {
	j.mu.Lock()
	j.status, j.phase = "done", "complete"
	j.mu.Unlock()
}

// jobStatus is the JSON snapshot returned by GET /api/ingest/status.
type jobStatus struct {
	JobID           string `json:"job_id"`
	Status          string `json:"status"`
	Phase           string `json:"phase"`
	Repo            string `json:"repo"`
	RootPath        string `json:"root_path"`
	FilesDiscovered int64  `json:"files_discovered"`
	FilesDone       int64  `json:"files_done"`
	ChunksEmbedded  int64  `json:"chunks_embedded"`
	Errors          int64  `json:"errors"`
	Error           string `json:"error,omitempty"`
}

func (j *Job) snapshot() jobStatus {
	j.mu.Lock()
	status, phase, root, errMsg := j.status, j.phase, j.rootPath, j.errMsg
	j.mu.Unlock()
	return jobStatus{
		JobID:           j.ID,
		Status:          status,
		Phase:           phase,
		Repo:            j.Repo,
		RootPath:        root,
		FilesDiscovered: atomic.LoadInt64(&j.stats.FilesDiscovered),
		FilesDone:       atomic.LoadInt64(&j.stats.FilesPersisted),
		ChunksEmbedded:  atomic.LoadInt64(&j.stats.ChunksEmbedded),
		Errors:          atomic.LoadInt64(&j.stats.Errors),
		Error:           errMsg,
	}
}

// jobManager holds in-flight + recent jobs in memory.
type jobManager struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func newJobManager() *jobManager { return &jobManager{jobs: map[string]*Job{}} }

func (m *jobManager) create(repo string) *Job {
	j := &Job{ID: randID(), Repo: repo, status: "queued", phase: "queued"}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.mu.Unlock()
	return j
}

func (m *jobManager) get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
