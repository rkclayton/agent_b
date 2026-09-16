package session

// WorkerJob is the item a c-role session is working on right now. It is the
// worker's whole brief: the item's own fields, and the repo it may write.
type WorkerJob struct {
	ItemID     string `json:"item_id"`
	Intent     string `json:"intent"`
	Approach   string `json:"approach"`
	Acceptance string `json:"acceptance"`
	Negative   string `json:"negative"`
	Repo       string `json:"repo"`
	// Verify is the item's verifier command. [x] is written only when it exits 0.
	Verify string `json:"verify,omitempty"`
}

// SetWorkerJob binds the session to one item. It is set before the run starts
// and cleared when the worker moves on, so the rendered prompt always describes
// the item actually in hand.
func (s *Session) SetWorkerJob(job WorkerJob) {
	s.mu.Lock()
	s.workerJob = job
	s.mu.Unlock()
}

func (s *Session) WorkerJob() WorkerJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workerJob
}

// IsWorker reports whether this session is a worker. A worker has no chat: it
// does not appear in the tab strip, and its only speech is a c.job post.
func (s *Session) IsWorker() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Role == "c"
}
