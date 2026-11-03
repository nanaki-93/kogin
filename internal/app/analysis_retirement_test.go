package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/storage"
)

func TestRetiredAnalysisRunsDoNotRestoreAuthority(t *testing.T) {
	for _, stage := range []string{"semantic", "performance"} {
		t.Run(stage, func(t *testing.T) {
			server, calls := analysisResponseServer(t, emptyAnalysisReply)
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			data := []byte(`{"schema_version":"1","status":"running","plan":{"compatibility_stage":"` + stage + `"}}`)
			path := filepath.Join(root, analysisRunRelativePath)
			if err := storage.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			run, err := service.currentTestAnalysisRun(context.Background())
			if err != nil || run != nil || calls.Load() != 0 {
				t.Fatalf("retired progress acquired authority: %+v, %v, calls %d", run, err, calls.Load())
			}
			retained, err := os.ReadFile(path)
			if err != nil || string(retained) != string(data) {
				t.Fatalf("passive read changed retired metadata: %s, %v", retained, err)
			}
			preview := analysisRunPreviewFor(t, service, AnalysisRunLimits{1, 30, 1}, nil)
			if _, err := service.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
				t.Fatal(err)
			}
			completedAnalysisRun(t, service)
			stored, err := os.ReadFile(path)
			if err != nil || !json.Valid(stored) || string(stored) == string(data) || calls.Load() == 0 {
				t.Fatalf("explicit unified start did not replace retired job: %v, calls %d", err, calls.Load())
			}
		})
	}
}
