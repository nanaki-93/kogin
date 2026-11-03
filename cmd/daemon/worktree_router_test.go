package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestWorktreeRouterKeepsConcurrentCheckoutResponsesSeparate(t *testing.T) {
	var router worktreeRouter
	var group sync.WaitGroup
	for _, name := range []string{"first checkout", "second checkout"} {
		root := t.TempDir()
		manager, err := project.NewManager(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Set(root, &project.Analysis{Path: root, Name: name}); err != nil {
			t.Fatal(err)
		}
		service, err := app.New(config.Default(), manager)
		if err != nil {
			t.Fatal(err)
		}
		index, err := manager.Index()
		if err != nil {
			t.Fatal(err)
		}
		query := url.Values{"project_id": {index.ProjectID}, "project_revision": {index.ProjectRevision}}
		response := httptest.NewRecorder()
		router.handler(service, manager).ServeHTTP(response, httptest.NewRequest("GET", "/api/projects/current/usage?"+query.Encode(), nil))
		var usage app.ChangeUsageReport
		if err := json.Unmarshal(response.Body.Bytes(), &usage); err != nil || response.Code != 200 || usage.ProjectID != index.ProjectID {
			t.Fatalf("checkout agent usage crossed scope: %s %v", response.Body.String(), err)
		}
		for range 8 {
			group.Add(1)
			go func() {
				defer group.Done()
				for range 10 {
					response := httptest.NewRecorder()
					router.handler(service, manager).ServeHTTP(response, httptest.NewRequest("GET", "/api/projects/current", nil))
					var analysis project.Analysis
					if err := json.Unmarshal(response.Body.Bytes(), &analysis); err != nil || analysis.Name != name {
						t.Errorf("checkout response crossed scope: %s, %v", response.Body.String(), err)
					}
				}
			}()
		}
	}
	group.Wait()
}

func BenchmarkWorktreeRouting(b *testing.B) {
	service, manager := &app.Service{}, &project.Manager{}
	b.Run("rebuild", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			newProjectHTTPMux(service, manager, testWriteToken.value)
		}
	})
	b.Run("reuse", func(b *testing.B) {
		var router worktreeRouter
		router.handler(service, manager)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			router.handler(service, manager)
		}
	})
}
