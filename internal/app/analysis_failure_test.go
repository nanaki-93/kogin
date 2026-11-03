package app

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/llm"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestModelFailuresAreNamedByKind(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://localhost:1234/v1", Err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
	for err, want := range map[error]string{
		fmt.Errorf("semantic: %w", refused): reasonModelUnreachable,
		&url.Error{Op: "Post", URL: "http://model.invalid", Err: &net.DNSError{Err: "no such host", Name: "model.invalid"}}: reasonModelUnreachable,
		&url.Error{Op: "Post", URL: "http://localhost:1234", Err: timeoutError{}}:                                           reasonModelTimedOut,
		fmt.Errorf("llm client: %w (status 404)", llm.ErrRequestRejected):                                                   reasonModelRejected,
		fmt.Errorf("llm client: %w: no content", llm.ErrUnusableResponse):                                                   reasonModelUnusable,
		fmt.Errorf("anything else"): reasonModelFailed,
	} {
		if got := modelFailureReason(err); got != want {
			t.Errorf("%v: %q", err, got)
		}
		if !analysisMetadataReason(modelFailureReason(err)) {
			t.Errorf("%q cannot be saved", modelFailureReason(err))
		}
	}
}

func TestAnalysisNamesAnUnreachableProvider(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "http://" + listener.Addr().String()
	_ = listener.Close() // Nothing listens there now.
	s, _ := newSemanticAnalysisService(t, address, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 60, 1}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	for _, file := range run.Files {
		for _, stage := range file.Stages {
			if stage.Status == AnalysisStageFailed && stage.Reason != reasonModelUnreachable {
				t.Fatalf("%s %s: %q", file.Path, stage.Stage, stage.Reason)
			}
		}
	}
	if run.Files[0].Stages[0].Status != AnalysisStageFailed {
		t.Fatalf("semantic stage: %+v", run.Files[0].Stages[0])
	}
}

// Runs saved with the wording that pointed to config.yaml still read, and show
// where to change models and time limits now.
func TestSavedFailureReasonsGetTheirCurrentWording(t *testing.T) {
	for legacy, current := range map[string]string{legacyReasonModelTimedOut: reasonModelTimedOut, legacyReasonModelUnreachable: reasonModelUnreachable, legacyReasonModelRejected: reasonModelRejected} {
		if !analysisMetadataReason(legacy) || currentModelFailureReason(legacy) != current || !strings.Contains(current, "Settings → Models & agents") {
			t.Fatalf("%q -> %q", legacy, currentModelFailureReason(legacy))
		}
		run := &AnalysisRun{Plan: AnalysisRunPreview{Files: []AnalysisPlannedFile{{Stages: []AnalysisStagePlan{{Eligible: true}}}}}, Files: []AnalysisRunFile{{Stages: []AnalysisStageProgress{{Stage: AnalysisStageSemantic, Status: AnalysisStageFailed, Reason: legacy}}}}}
		normalizeAnalysisRunEligibility(run)
		if run.Files[0].Stages[0].Reason != current {
			t.Fatalf("saved reason kept its old wording: %q", run.Files[0].Stages[0].Reason)
		}
	}
}
