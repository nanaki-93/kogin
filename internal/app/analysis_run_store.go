package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

const analysisRunRelativePath = ".mini-orca/analysis/run.json"
const maxAnalysisRunMetadataBytes = 64 * 1024 * 1024

var errAnalysisRunCorrupt = errors.New("saved analysis progress is unreadable")

// ErrNewerAnalysisRun identifies saved analysis progress written by a newer
// Mini-Orca. It is never set aside: upgrading reads it again.
var ErrNewerAnalysisRun = errors.New("saved analysis progress was written by a newer Mini-Orca; upgrade Mini-Orca to use it, or move it away")

func cloneAnalysisPreview(plan AnalysisRunPreview) AnalysisRunPreview {
	copy := plan
	copy.Models = cloneAnalysisModels(plan.Models)
	if plan.Features != nil {
		features := *plan.Features
		features.ExcludedPaths = append([]string{}, plan.Features.ExcludedPaths...)
		copy.Features = &features
	}
	if plan.Docs != nil {
		docs := *plan.Docs
		copy.Docs = &docs
	}
	copy.Files = append([]AnalysisPlannedFile{}, plan.Files...)
	for i := range copy.Files {
		copy.Files[i].Stages = append([]AnalysisStagePlan{}, plan.Files[i].Stages...)
	}
	copy.Excluded = append([]AnalysisExcludedFile{}, plan.Excluded...)
	copy.Providers = append([]AnalysisProviderRequirement{}, plan.Providers...)
	for i := range copy.Providers {
		copy.Providers[i].Stages = append([]AnalysisStage{}, plan.Providers[i].Stages...)
	}
	return copy
}

func cloneAnalysisRun(run *AnalysisRun) *AnalysisRun {
	if run == nil {
		return nil
	}
	copy := *run
	if run.Features != nil {
		features := *run.Features
		if features.SuggestionCount != nil {
			count := *features.SuggestionCount
			features.SuggestionCount = &count
		}
		copy.Features = &features
	}
	if run.Docs != nil {
		docs := *run.Docs
		copy.Docs = &docs
	}
	copy.Plan = cloneAnalysisPreview(run.Plan)
	copy.Files = append([]AnalysisRunFile{}, run.Files...)
	for i := range copy.Files {
		copy.Files[i].Stages = append([]AnalysisStageProgress{}, run.Files[i].Stages...)
		for j := range copy.Files[i].Stages {
			if count := copy.Files[i].Stages[j].FindingCount; count != nil {
				n := *count
				copy.Files[i].Stages[j].FindingCount = &n
			}
		}
	}
	copy.Sections = append([]AnalysisSectionProgress{}, run.Sections...)
	for i := range copy.Sections {
		if count := copy.Sections[i].FindingCount; count != nil {
			n := *count
			copy.Sections[i].FindingCount = &n
		}
	}
	return &copy
}

func loadAnalysisRun(root string) (*AnalysisRun, error) {
	path, err := storage.MetadataPath(root, analysisRunRelativePath)
	if err != nil {
		return nil, fmt.Errorf("read analysis progress: %w", err)
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read analysis progress: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAnalysisRunMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAnalysisRunMetadataBytes {
		return nil, errAnalysisRunCorrupt
	}
	return decodeCompatibleAnalysisHistory(data)
}

func (s *Service) restoreAnalysisRunLocked() error {
	c := s.analysisRun
	root := s.manager.Root()
	if root == "" {
		return nil
	}
	if c.root == root && c.run != nil {
		return c.fault
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.fault != nil && c.run != nil {
		if c.detached == nil {
			c.detached = make(map[string]analysisRunRecovery)
		}
		c.detached[c.root] = analysisRunRecovery{run: cloneAnalysisRun(c.run), fault: c.fault}
	}
	c.run = nil
	c.fault = nil
	c.root = root
	c.windowStart = time.Time{}
	if retained, ok := c.detached[root]; ok {
		c.run, c.fault = retained.run, retained.fault
		delete(c.detached, root)
		return c.fault
	}
	run, err := loadAnalysisRun(root)
	if errors.Is(err, errAnalysisRunCorrupt) {
		// Unreadable progress must not block the overview or a new analysis.
		if setAsideErr := quarantineAnalysisRun(root); setAsideErr != nil {
			return errors.Join(err, setAsideErr)
		}
		run, err = nil, nil
	}
	if err != nil {
		return err
	}
	c.run = run
	if run == nil {
		return nil
	}
	switch run.Status {
	case AnalysisRunQueued, AnalysisRunRunning, AnalysisRunPausing:
		run.Status = AnalysisRunInterrupted
		run.Reason = "Analysis was interrupted; preview and resume explicitly."
		interruptAnalysisStages(run, AnalysisStageInterrupted)
		return s.saveAnalysisRunLocked(false)
	case AnalysisRunCanceling:
		run.Status = AnalysisRunCanceled
		interruptAnalysisStages(run, AnalysisStageCanceled)
		return s.saveAnalysisRunLocked(false)
	}
	return nil
}

func (s *Service) saveAnalysisRunLocked(recoverFault bool) error {
	c := s.analysisRun
	if c.fault != nil && !recoverFault {
		return errAnalysisRunPersistence
	}
	s.updateAnalysisElapsedLocked()
	c.run.UpdatedAt = time.Now().UTC()
	refreshAnalysisSections(c.run)
	err := validateStoredAnalysisRun(c.run)
	var data []byte
	if err == nil {
		data, err = json.MarshalIndent(c.run, "", "  ")
	}
	if err == nil && len(data) > maxAnalysisRunMetadataBytes {
		err = fmt.Errorf("analysis progress exceeds metadata size limit")
	}
	var targetPath string
	if err == nil {
		targetPath, err = storage.MetadataPath(c.root, analysisRunRelativePath)
	}
	if err == nil {
		write := s.writeAnalysisRun
		if write == nil {
			write = storage.WriteFile
		}
		err = write(targetPath, data, 0600)
	}
	if err != nil {
		s.failAnalysisPersistenceLocked()
		return errAnalysisRunPersistence
	}
	c.fault = nil
	s.notifyEvent("analysis")
	return nil
}

func (s *Service) failAnalysisPersistenceLocked() {
	c := s.analysisRun
	c.fault = errAnalysisRunPersistence
	if c.run.Status != AnalysisRunStale {
		c.run.Status = AnalysisRunInterrupted
	}
	c.run.Reason = "Analysis progress could not be saved; resume or cancel to recover."
	interruptAnalysisStages(c.run, AnalysisStageInterrupted)
	if c.cancel != nil {
		c.cancel()
	}
}

func analysisMetadataPath(value string) bool {
	return value != "" && len(value) <= 4096 && path.Clean(value) == value && !path.IsAbs(value) && value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, "\\\x00\r\n")
}

func analysisMetadataReason(value string) bool {
	if project.PerformanceReviewFailure(value).Valid() || slices.Contains(modelFailureReasons, value) || slices.Contains(carriedAnalysisReasons, value) {
		return true
	}
	switch value {
	case "The provider rejected the Performance response format.", analysisSelectionExclusion, analysisRetryExclusion, analysisRecoveryExclusion, analysisStaleExclusion, analysisStalePathExclusion, "", "Excluded by source policy.", "Not a supported text source file.", "Source exceeds analyzer size limits.", "Not eligible for semantic source analysis.",
		"Passive security rules require a Go source file.", "The model for this stage is not configured.",
		"Analysis stage could not complete.", "The stage needs an additional attempt allowance.", "The file is not eligible for this source analysis.",
		"The report contains incomplete evidence; review its details.",
		"Analysis canceled by the user.", "Analysis paused by the user.", "The batch limit was reached; resume to continue pending files.",
		"The stage exhausted its total attempt allowance.", "The dispatch allowance ended; preview and resume the remaining work.",
		"The file dispatch allowance ended; preview and resume the remaining work.",
		"File analysis reached its time budget. Feature discovery continues independently.",
		"File analysis reached its allowance. Feature discovery continues independently.",
		"Feature discovery reached its allowance; preview and resume to try again.", analysisFeatureHistoryFullReason,
		"Analysis stopped before the stage completed; review and resume.", "Project source, policy or provider identity changed; start a new analysis.",
		"Analysis progress could not be saved; resume or cancel to recover.", "Analysis was interrupted; preview and resume explicitly.",
		docsRefreshBusyReason, docsRefreshUnreadableReason, docsRefreshUnsavedReason, docsRefreshUnconfirmedReason, docsRefreshStoppedReason:
		return true
	}
	return false
}

func validateStoredAnalysisRun(run *AnalysisRun) error {
	if !validStoredAnalysisIdentity(run) || !validStoredAnalysisWindow(run) {
		return errAnalysisRunCorrupt
	}
	queue, err := analysisQueueFingerprint(&run.Plan)
	if err != nil || queue != run.Identity.QueueID {
		return errAnalysisRunCorrupt
	}
	totalFindings, err := validateStoredAnalysisFiles(run)
	if err != nil {
		return err
	}
	for _, file := range run.Plan.Excluded {
		if !analysisMetadataPath(file.Path) || !analysisMetadataReason(file.Reason) {
			return errAnalysisRunCorrupt
		}
	}
	if err := validateStoredAnalysisSections(run, totalFindings); err != nil {
		return err
	}
	if err := validateStoredAnalysisFeatures(run); err != nil {
		return err
	}
	if err := validateStoredAnalysisDocs(run); err != nil {
		return err
	}
	switch run.Status {
	case AnalysisRunCompleted, AnalysisRunCompletedEmpty, AnalysisRunPartial, AnalysisRunFailed, AnalysisRunUnavailable:
		if analysisFinishedStatus(run) != run.Status {
			return errAnalysisRunCorrupt
		}
	}
	return nil
}

func validStoredAnalysisIdentity(run *AnalysisRun) bool {
	if run == nil || run.SchemaVersion != AnalysisRunSchemaVersion || run.Identity.Validate() != nil || !run.Status.Valid() {
		return false
	}
	if run.Plan.SchemaVersion != AnalysisRunSchemaVersion || run.Plan.Scope != AnalysisRunScopeProject || run.Plan.Identity != run.Identity.AnalysisQueueIdentity {
		return false
	}
	return validStoredAnalysisIdentityPlan(&run.Plan) && analysisMetadataReason(run.Reason)
}

func validStoredAnalysisIdentityPlan(plan *AnalysisRunPreview) bool {
	if validateAnalysisSelectionOptions(plan.RecoverIncomplete, plan.Refresh, plan.ClearResults, plan.RetryStaleFailed, plan.StaleOnly, plan.StalePath) != nil {
		return false
	}
	// A saved run with feature discovery was never a scoped recovery.
	if plan.Features != nil && (plan.StaleOnly || plan.RecoverIncomplete) {
		return false
	}
	if plan.RetryStaleFailed && plan.Refresh {
		return false
	}
	if plan.RecoverIncomplete && (plan.Refresh || plan.RetryStaleFailed || plan.Features != nil) {
		return false
	}
	return plan.Models.Validate() == nil && plan.Limits.Validate() == nil && plan.PreviewID != ""
}

func validStoredAnalysisWindow(run *AnalysisRun) bool {
	return !run.CreatedAt.IsZero() && !run.UpdatedAt.Before(run.CreatedAt) && run.ElapsedSeconds >= 0 && run.WindowElapsedSeconds >= 0 &&
		(run.Plan.Features != nil || run.WindowElapsedSeconds <= int64(run.Plan.Limits.BudgetSeconds)) && run.ElapsedSeconds >= run.WindowElapsedSeconds &&
		run.WindowFilesCompleted >= 0 && run.WindowFilesCompleted <= run.Plan.Limits.BatchFiles && len(run.Files) == len(run.Plan.Files) && len(run.Sections) == 3
}

func validateStoredAnalysisFiles(run *AnalysisRun) (int, error) {
	total := 0
	for i, file := range run.Files {
		planned := run.Plan.Files[i]
		if run.Plan.StalePath != "" && file.Path != run.Plan.StalePath && !planned.Retained {
			return 0, errAnalysisRunCorrupt
		}
		if !validStoredAnalysisFile(file, planned) || i > 0 && run.Files[i-1].Path >= file.Path {
			return 0, errAnalysisRunCorrupt
		}
		for j, stage := range file.Stages {
			if !validStoredRetention(planned, planned.Stages[j]) {
				return 0, errAnalysisRunCorrupt
			}
			count, err := validateStoredAnalysisStage(stage, planned.Stages[j], analysisStages[j], run.Plan.Limits.MaxAttemptsPerStage)
			if err != nil {
				return 0, err
			}
			total += count
		}
	}
	return total, nil
}

// Retained files never dispatch; only their stages without a saved report carry an outcome.
func validStoredRetention(file AnalysisPlannedFile, stage AnalysisStagePlan) bool {
	if !file.Retained {
		return stage.Carried == ""
	}
	return stage.MaxModelRequests == 0 && (stage.Carried == "" || validCarriedAnalysisStatus(stage.Carried) && stage.Eligible && !stage.Cached)
}

func validStoredAnalysisFile(file AnalysisRunFile, planned AnalysisPlannedFile) bool {
	return file.AnalysisFileIdentity == planned.AnalysisFileIdentity && analysisMetadataPath(file.Path) && file.ContentHash != "" && file.Language != "" &&
		planned.SizeBytes >= 0 && len(file.Stages) == len(analysisStages) && len(planned.Stages) == len(analysisStages)
}

func validateStoredAnalysisStage(stage AnalysisStageProgress, plan AnalysisStagePlan, expected AnalysisStage, maxAttempts int) (int, error) {
	if stage.Stage != expected || plan.Stage != stage.Stage || stage.Attempts < 0 || stage.Attempts > maxAttempts ||
		stage.Stage == AnalysisStageSecurityRules && stage.Attempts != 0 || !analysisMetadataReason(stage.Reason) || !analysisMetadataReason(plan.Reason) ||
		plan.MaxModelRequests < 0 || plan.MaxModelRequests > maxAttempts {
		return 0, errAnalysisRunCorrupt
	}
	return validateStoredStageEvidence(stage)
}

func validateStoredStageEvidence(stage AnalysisStageProgress) (int, error) {
	evidence := false
	switch stage.Status {
	case AnalysisStageCompleted, AnalysisStageCompletedEmpty, AnalysisStagePartial:
		evidence = true
	case AnalysisStagePending, AnalysisStageRunning, AnalysisStageFailed, AnalysisStageSkipped, AnalysisStageUnavailable, AnalysisStageCanceled, AnalysisStageInterrupted, AnalysisStageStale:
	default:
		return 0, errAnalysisRunCorrupt
	}
	if evidence != (stage.FindingCount != nil) || !evidence && (stage.Cached || stage.ReportID != "") {
		return 0, errAnalysisRunCorrupt
	}
	if !evidence {
		return 0, nil
	}
	count := *stage.FindingCount
	if count < 0 || count > 32 || len(stage.ReportID) != 64 || stage.Status == AnalysisStageCompletedEmpty && count != 0 || stage.Status == AnalysisStageCompleted && count == 0 {
		return 0, errAnalysisRunCorrupt
	}
	return count, nil
}

func validateStoredAnalysisSections(run *AnalysisRun, totalFindings int) error {
	recomputed := cloneAnalysisRun(run)
	refreshAnalysisSections(recomputed)
	if !reflect.DeepEqual(run.Sections, recomputed.Sections) {
		refreshAnalysisSectionsWithEligibility(recomputed, true)
		if !reflect.DeepEqual(run.Sections, recomputed.Sections) {
			return errAnalysisRunCorrupt
		}
	}
	sectionFindings := 0
	for _, section := range run.Sections {
		if section.Validate() != nil {
			return errAnalysisRunCorrupt
		}
		if section.FindingCount != nil {
			sectionFindings += *section.FindingCount
		}
	}
	if totalFindings != sectionFindings {
		return errAnalysisRunCorrupt
	}
	return nil
}

func normalizeAnalysisRunEligibility(run *AnalysisRun) {
	for i := range run.Files {
		for j := range run.Files[i].Stages {
			stage := &run.Files[i].Stages[j]
			// The plan keeps its admitted wording; it is part of the queue identity.
			stage.Reason = currentModelFailureReason(stage.Reason)
			if !run.Plan.Files[i].Stages[j].Eligible && stage.Status == AnalysisStageUnavailable {
				stage.Status = AnalysisStageSkipped
			}
		}
	}
	refreshAnalysisSections(run)
	switch run.Status {
	case AnalysisRunCompleted, AnalysisRunCompletedEmpty, AnalysisRunPartial, AnalysisRunFailed, AnalysisRunUnavailable:
		run.Status = analysisFinishedStatus(run)
		refreshAnalysisSections(run)
	}
}
