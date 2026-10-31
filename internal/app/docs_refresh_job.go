package app

import (
	"context"
	"errors"
	"sync"
	"time"
)

// docsRefreshJob is the refresh started from the Docs page. It runs in the
// background, since a summary per component can take minutes, and its latest
// outcome is read with the project overview.
type docsRefreshJob struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	projectID string
	latest    *DocsRefresh
}

// errDocsModelMissing is returned when no analyze model can write the docs.
var errDocsModelMissing = errors.New("the analyze model is not configured")

// StartDocsRefresh starts writing the docs' model-written parts again with
// the analyze model: the summaries of the components whose files' summaries
// changed, then the project overview. It returns the running refresh.
func (s *Service) StartDocsRefresh(_ context.Context, request DocsRefreshRequest) (*DocsRefresh, error) {
	if err := s.requireActiveRevision(request.ProjectID, request.ProjectRevision); err != nil {
		return nil, err
	}
	runtime := s.conf().runtimes.analyze
	if runtime.client == nil {
		return nil, errDocsModelMissing
	}
	if err := requireModelRuntimeConfirmation(runtime, request.ConfirmRemoteProvider); err != nil {
		return nil, err
	}
	job := &s.docsJob
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.cancel != nil {
		return nil, ErrDocsRefreshBusy
	}
	worker, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	running := DocsRefresh{Status: "running", Overview: "skipped", At: time.Now().UTC()}
	job.cancel, job.done, job.projectID, job.latest = cancel, done, request.ProjectID, &running
	go func() {
		defer close(done)
		defer cancel()
		refresh, _ := s.refreshDocs(worker, runtime, true)
		job.mu.Lock()
		if job.done == done {
			job.cancel, job.latest = nil, &refresh
		}
		job.mu.Unlock()
		s.notifyEvent("docs")
	}()
	return &running, nil
}

// latestDocsRefresh is the Docs page's latest refresh of the project.
func (s *Service) latestDocsRefresh(projectID string) *DocsRefresh {
	job := &s.docsJob
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.latest == nil || job.projectID != projectID {
		return nil
	}
	latest := *job.latest
	return &latest
}

// cancelDocsRefresh stops a running refresh; the summaries it wrote stay.
func (s *Service) cancelDocsRefresh() {
	s.docsJob.mu.Lock()
	cancel := s.docsJob.cancel
	s.docsJob.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// waitDocsRefreshDone stops a running refresh and waits for it to end.
func (s *Service) waitDocsRefreshDone(ctx context.Context) error {
	s.docsJob.mu.Lock()
	done, cancel := s.docsJob.done, s.docsJob.cancel
	s.docsJob.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
