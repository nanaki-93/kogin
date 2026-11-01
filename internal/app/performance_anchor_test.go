package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestPerformanceRetryCompletesRangesSpanningDeclarations(t *testing.T) {
	type declSpan struct {
		name      string
		startLine int
		endLine   int
	}
	for _, test := range []struct {
		name        string
		start, end  int
		wrongSymbol string
		spans       []declSpan
	}{
		{"command and helper", 38, 57, "logResult", []declSpan{{name: "command", startLine: 12, endLine: 45}, {name: "logResult", startLine: 47, endLine: 64}}},
		{"logging methods", 39, 65, "Info", []declSpan{{name: "Info", startLine: 39, endLine: 44}, {name: "Warn", startLine: 46, endLine: 51}, {name: "Error", startLine: 53, endLine: 58}, {name: "Debug", startLine: 60, endLine: 65}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lines := make([]string, 69)
			lines[0] = "package main"
			for _, span := range test.spans {
				lines[span.startLine-1] = "func " + span.name + "() {"
				lines[span.endLine-1] = "}"
			}
			currentReply := strings.Replace(validPerformanceLineReview, `"start_line":5,"end_line":5`, fmt.Sprintf(`"start_line":%d,"end_line":%d`, test.start, test.end), 1)
			legacyReply := strings.Replace(currentReply, `"title":`, `"symbol":"`+test.wrongSymbol+`","title":`, 1)
			var current atomic.Bool
			server, calls := analysisResponseServer(t, func(stage AnalysisStage) string {
				if stage != AnalysisStagePerformance {
					return emptyAnalysisReply(stage)
				}
				if current.Load() {
					return currentReply
				}
				return legacyReply
			})
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Reindex(); err != nil {
				t.Fatal(err)
			}
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 1}, nil)
			if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
				t.Fatal(err)
			}
			failed := completedAnalysisRun(t, s)
			if failed.Files[0].Stages[1].Status != AnalysisStageFailed || failed.Files[0].Stages[1].Reason != project.PerformanceInvalidJSON.Error() {
				t.Fatalf("old response did not reproduce failure: %+v", failed)
			}
			snapshot, err := s.preparePerformanceReview("main.go")
			if err != nil {
				t.Fatal(err)
			}
			schema := compileReviewSchema(t, performanceReviewResponseSchema(snapshot).Schema)
			assertReviewSchemaAccepts(t, schema, legacyReply, false)
			assertReviewSchemaAccepts(t, schema, currentReply, true)
			current.Store(true)
			retry := retryPreviewFor(t, s, nil)
			if retry.ExpectedModelRequests != 1 {
				t.Fatalf("retry=%+v", retry)
			}
			if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(retry)); err != nil {
				t.Fatal(err)
			}
			completed := completedAnalysisRun(t, s)
			if completed.Status != AnalysisRunCompleted || calls.Load() != 4 {
				t.Fatalf("completion=%+v calls=%d", completed, calls.Load())
			}
			_, _, policy, err := s.analysisInputs()
			if err != nil {
				t.Fatal(err)
			}
			report, err := project.LoadPerformanceFileReport(root, "main.go", snapshot.file.ContentHash, policy)
			if err != nil || report == nil || len(report.Findings) != 1 || report.Warning != "" {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			finding := report.Findings[0]
			if finding.StartLine != test.start || finding.EndLine != test.end {
				t.Fatalf("range was altered: %+v", finding)
			}
			s.analysisRun = &analysisRunController{}
			assertSelectionStages(t, readSelectionFor(t, s), "main.go", "fresh", "up to date")
			if next := retryPreviewFor(t, s, nil); len(scopedFiles(next)) != 0 || calls.Load() != 4 {
				t.Fatalf("completed range still needs retry: %+v", next)
			}
		})
	}
}
