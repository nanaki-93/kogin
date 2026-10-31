package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

type sseTestRecorder struct {
	mu      sync.Mutex
	header  http.Header
	code    int
	body    bytes.Buffer
	flushed chan string
}

func newSSETestRecorder() *sseTestRecorder {
	return &sseTestRecorder{
		header:  make(http.Header),
		code:    http.StatusOK,
		flushed: make(chan string, 8),
	}
}

func (r *sseTestRecorder) Header() http.Header {
	return r.header
}

func (r *sseTestRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.code = statusCode
}

func (r *sseTestRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(p)
}

func (r *sseTestRecorder) Flush() {
	r.mu.Lock()
	snapshot := r.body.String()
	r.mu.Unlock()
	select {
	case r.flushed <- snapshot:
	default:
	}
}

func TestProjectEventsSSEStream(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.py", "def run():\n    return 1\n")
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig("http://127.0.0.1:1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.TrustProjectExecution(context.Background(), index.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	handler := NewProjectHandler(manager, service)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/current/events", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")
	rec := newSSETestRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.Events(rec, req)
	}()

	readySnapshot := <-rec.flushed
	if !strings.Contains(readySnapshot, "event: ready") {
		t.Fatalf("SSE initial flush missing ready event: %s", readySnapshot)
	}

	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	eventSnapshot := <-rec.flushed
	cancel()
	<-done

	if rec.code != http.StatusOK {
		t.Fatalf("SSE status = %d", rec.code)
	}
	if !strings.Contains(eventSnapshot, "event: update") || !strings.Contains(eventSnapshot, `"kind":"project_index"`) {
		t.Fatalf("SSE stream missing reindex update event: %s", eventSnapshot)
	}
}
