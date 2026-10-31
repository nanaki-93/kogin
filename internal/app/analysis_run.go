package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

type analysisRunRecovery struct {
	run   *AnalysisRun
	fault error
}

type analysisRunController struct {
	detached          map[string]analysisRunRecovery
	mu                sync.Mutex
	run               *AnalysisRun
	root              string
	fault             error
	done              chan struct{}
	cancel            context.CancelFunc
	featureCancel     context.CancelFunc
	admission         *AnalysisRunPreview
	confirmations     AnalysisRunConfirmations
	windowStart       time.Time
	elapsedBase       int64
	windowBudget      time.Duration
	fileStopReason    string
	featureStopReason string
}

func (s *Service) StartAnalysisRun(ctx context.Context, request AnalysisRunStartRequest) (*AnalysisRun, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := s.validateAnalysisModelSelections(ctx, request.Models, nil); err != nil {
		return nil, err
	}
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	if request.ClearResults {
		s.changesMu.Lock()
		defer s.changesMu.Unlock()
	}
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	return s.startAnalysisRunLocked(ctx, request)
}

func canReplaceAnalysisRun(run *AnalysisRun, clearResults bool) bool {
	if run == nil {
		return true
	}
	switch run.Status {
	case AnalysisRunRunning, AnalysisRunQueued, AnalysisRunPausing, AnalysisRunCanceling:
		return false
	case AnalysisRunPaused, AnalysisRunInterrupted:
		return clearResults
	default:
		return true
	}
}

// Caller holds both lifecycle and controller locks.
func (s *Service) startAnalysisRunLocked(ctx context.Context, request AnalysisRunStartRequest) (*AnalysisRun, error) {
	c := s.analysisRun
	if err := s.restoreAnalysisRunLocked(); err != nil {
		return nil, err
	}
	if c.done != nil {
		return nil, errAnalysisRunBusy
	}
	if c.fault != nil {
		return cloneAnalysisRun(c.run), errAnalysisRunPersistence
	}
	if !canReplaceAnalysisRun(c.run, request.ClearResults) {
		return cloneAnalysisRun(c.run), errAnalysisRunBusy
	}
	preview, err := s.analysisPreviewLocked(ctx, AnalysisPreviewRequest{Models: request.Models, IncludeFeatures: request.IncludeFeatures, ProjectID: request.Identity.ProjectID, ProjectRevision: request.Identity.ProjectRevision, Scope: AnalysisRunScopeProject, RetryStaleFailed: request.RetryStaleFailed, StaleOnly: request.StaleOnly, StalePath: request.StalePath, RecoverIncomplete: request.RecoverIncomplete, Refresh: request.Refresh, ClearResults: request.ClearResults, Limits: request.Limits})
	if err != nil {
		return nil, err
	}
	if preview.Identity != request.Identity || preview.PreviewID != request.PreviewID {
		return nil, project.ErrRevisionConflict
	}
	if err := validateAnalysisConfirmations(preview, request.Confirmations); err != nil {
		return nil, err
	}
	if err := s.validateAnalysisQueue(ctx, s.manager.Root(), preview, true); err != nil {
		return nil, err
	}
	if request.ClearResults {
		if err := s.clearAnalysisResultsLocked(ctx, s.manager.Root(), preview); err != nil {
			return nil, err
		}
	}
	run := newAnalysisRun(*preview)
	if err := s.recordSavedAnalysisStages(run); err != nil {
		return nil, err
	}
	c.root = s.manager.Root()
	c.run = run
	if err := s.saveAnalysisRunLocked(false); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	result := cloneAnalysisRun(c.run)
	if err := s.launchAnalysisRunLocked(preview, request.Confirmations); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	return result, nil
}

func (s *Service) inProgressFindingTargets(ctx context.Context, root, projectID string) []project.FindingTarget {
	raw := collectSessionFindingTargets(root, false)
	if trees, err := readGitWorktrees(ctx, root); err == nil {
		for _, tree := range trees {
			if !tree.Current && !tree.Prunable {
				if canonical, err := project.CanonicalRoot(tree.Path); err == nil {
					raw = append(raw, collectSessionFindingTargets(canonical, true)...)
				}
			}
		}
	}
	seen := make(map[project.FindingTarget]bool, len(raw))
	var targets []project.FindingTarget
	for _, target := range raw {
		if (projectID == "" || target.ProjectID == projectID) && !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	return targets
}

func collectSessionFindingTargets(root string, includeApplied bool) []project.FindingTarget {
	sessions, err := listChangeSessions(root)
	if err != nil {
		return nil
	}
	var targets []project.FindingTarget
	for _, entry := range sessions {
		if entry.State == "draft" || (includeApplied && entry.State == "applied") {
			targets = append(targets, entry.FindingTargets...)
		}
	}
	return targets
}

func (s *Service) syncSemanticFindingsLocked(root string) {
	analysis, index, input, err := s.workspaceState()
	if err != nil {
		return
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return
	}
	cache, err := project.NewFileAnalysisCache(root)
	if err != nil {
		return
	}
	cache = cache.WithPolicy(policy)
	var models *AnalysisModels
	if s.analysisRun.run != nil && s.analysisRun.run.Identity.ProjectID == analysis.ProjectID {
		models = s.analysisRun.run.Plan.Models
	}
	runtime := s.analysisModelRuntime(AnalysisStageSemantic, models)
	reported := project.SuggestedFindingsForProject(analysis.Report)
	for i := range index.Files {
		file := &index.Files[i]
		if file.Binary {
			continue
		}
		cached, err := cache.Load(semanticCacheInputForRuntime(analysis, file, file.ContentHash, policy.Version(), runtime))
		if err == nil && cached != nil {
			reported = append(reported, project.SuggestedFindingsForFile(*cached)...)
		}
	}
	if store, err := project.NewFindingStore(root); err == nil {
		_, _ = store.ReconcileSource(input, project.FindingSourceAI, reported)
	}
}

func (s *Service) clearAnalysisResultsLocked(ctx context.Context, root string, preview *AnalysisRunPreview) error {
	targets := s.inProgressFindingTargets(ctx, root, preview.Identity.ProjectID)
	if len(targets) > 0 {
		s.syncSemanticFindingsLocked(root)
	}
	fileCache, err := project.NewFileAnalysisCache(root)
	if err != nil {
		return err
	}
	if err := fileCache.Clear(); err != nil {
		return err
	}
	if err := project.ClearPerformanceFileReports(root); err != nil {
		return err
	}
	securityCache, err := project.NewSecurityReportCache(root)
	if err != nil {
		return err
	}
	if err := securityCache.Clear(); err != nil {
		return err
	}
	findingStore, err := project.NewFindingStore(root)
	if err != nil {
		return err
	}
	if err := findingStore.ClearExcept(targets); err != nil {
		return err
	}
	if err := s.manager.ResetFileAnalysisStatuses(preview.Identity.ProjectID, preview.Identity.ProjectRevision); err != nil {
		return err
	}
	index, err := s.manager.Index()
	if err != nil {
		return err
	}
	report, err := clearFeatureSuggestions(root, index)
	if err != nil {
		return err
	}
	if preview.Features != nil {
		preview.Features.ExpectedHash = report.Hash
	}
	s.notifyEvent("findings")
	s.notifyEvent("features")
	return nil
}

// The caller holds jobLifecycleMu through its complete project read.
func (s *Service) currentAnalysisRunLocked(ctx context.Context) (*AnalysisRun, error) {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.restoreAnalysisRunLocked(); err != nil && (c.run == nil || c.fault == nil) {
		return cloneAnalysisRun(c.run), err
	}
	if c.run == nil {
		return nil, nil
	}
	if err := s.refreshAnalysisRunFreshnessLocked(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return cloneAnalysisRun(c.run), err
	}
	s.updateAnalysisElapsedLocked()
	return cloneAnalysisRun(c.run), c.fault
}

func (s *Service) ControlAnalysisRun(ctx context.Context, request AnalysisRunControlRequest) (*AnalysisRun, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.Action == AnalysisRunResume {
		if err := s.validateAnalysisModelSelections(ctx, nil, &request.Identity); err != nil {
			return nil, err
		}
	}
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	// Recoverable write failures retain the in-memory state; only explicit controls
	// may attempt another save. Restore errors without a retained run remain fatal.
	if err := s.restoreAnalysisRunLocked(); err != nil && (c.run == nil || c.fault == nil) {
		return nil, err
	}
	if c.run == nil || c.run.Identity != request.Identity {
		return nil, project.ErrRevisionConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch request.Action {
	case AnalysisRunCancel:
		return s.cancelAnalysisRunLocked()
	case AnalysisRunPause:
		return s.pauseAnalysisRunLocked()
	case AnalysisRunResume:
		return s.resumeAnalysisRunLocked(ctx, request)
	}
	return cloneAnalysisRun(c.run), nil
}

func (s *Service) cancelAnalysisRunLocked() (*AnalysisRun, error) {
	c := s.analysisRun
	if c.run.Status == AnalysisRunStale {
		if c.fault == nil {
			return cloneAnalysisRun(c.run), project.ErrRevisionConflict
		}
		// Discarding stale work must remain possible after a failed save.
		// Recovery preserves its stale identity and cannot restart requests.
		c.run.Reason = "Project source, policy or provider identity changed; start a new analysis."
		err := s.saveAnalysisRunLocked(true)
		return cloneAnalysisRun(c.run), err
	}
	c.run.Status = AnalysisRunCanceled
	if c.done != nil {
		c.run.Status = AnalysisRunCanceling
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.run.Reason = "Analysis canceled by the user."
	interruptAnalysisStages(c.run, AnalysisStageCanceled)
	if err := s.saveAnalysisRunLocked(true); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	return cloneAnalysisRun(c.run), nil
}

func (s *Service) pauseAnalysisRunLocked() (*AnalysisRun, error) {
	c := s.analysisRun
	if c.run.Status != AnalysisRunRunning && c.run.Status != AnalysisRunQueued && c.run.Status != AnalysisRunPausing && c.run.Status != AnalysisRunPaused && c.fault == nil {
		return nil, fmt.Errorf("%w: analysis cannot be paused in its current state", project.ErrRevisionConflict)
	}
	c.run.Status = AnalysisRunPaused
	if c.done != nil && c.fault == nil {
		c.run.Status = AnalysisRunPausing
	}
	if c.featureCancel != nil {
		c.featureCancel()
	}
	c.run.Reason = "Analysis paused by the user."
	if err := s.saveAnalysisRunLocked(true); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	return cloneAnalysisRun(c.run), nil
}

func (s *Service) resumeAnalysisRunLocked(ctx context.Context, request AnalysisRunControlRequest) (*AnalysisRun, error) {
	c := s.analysisRun
	if c.done != nil {
		return cloneAnalysisRun(c.run), errAnalysisRunBusy
	}
	if c.run.Status != AnalysisRunPaused && c.run.Status != AnalysisRunInterrupted {
		return nil, fmt.Errorf("%w: analysis requires paused or interrupted progress to resume", project.ErrRevisionConflict)
	}
	preview, err := s.analysisPreviewLocked(ctx, AnalysisPreviewRequest{ProjectID: c.run.Identity.ProjectID, ProjectRevision: c.run.Identity.ProjectRevision, Scope: AnalysisRunScopeProject, RetryStaleFailed: c.run.Plan.RetryStaleFailed, StaleOnly: c.run.Plan.StaleOnly, StalePath: c.run.Plan.StalePath, RecoverIncomplete: c.run.Plan.RecoverIncomplete, Refresh: c.run.Plan.Refresh, Limits: c.run.Plan.Limits, ResumeRun: &request.Identity})
	if err != nil {
		return nil, err
	}
	if request.PreviewID != preview.PreviewID {
		return nil, project.ErrRevisionConflict
	}
	if err := validateAnalysisConfirmations(preview, *request.Confirmations); err != nil {
		return nil, err
	}
	if err := s.validateAnalysisQueue(ctx, c.root, preview, true); err != nil {
		return nil, err
	}
	c.run.Identity.Generation = newOpaqueID("generation")
	c.run.Status = AnalysisRunQueued
	c.run.Reason = ""
	c.run.WindowElapsedSeconds = 0
	c.run.WindowFilesCompleted = 0
	if err := s.saveAnalysisRunLocked(true); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	result := cloneAnalysisRun(c.run)
	if err := s.launchAnalysisRunLocked(preview, *request.Confirmations); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	return result, nil
}

func (s *Service) launchAnalysisRunLocked(preview *AnalysisRunPreview, confirmations AnalysisRunConfirmations) error {
	c := s.analysisRun
	c.windowBudget = time.Duration(c.run.Plan.Limits.BudgetSeconds) * time.Second
	ctx, cancel, err := s.analysisAgentContext(context.Background(), preview)
	if err != nil {
		c.run.Status = AnalysisRunInterrupted
		c.run.Reason = "Analysis stopped before the stage completed; review and resume."
		return errors.Join(err, s.saveAnalysisRunLocked(false))
	}
	c.cancel = cancel
	c.done = make(chan struct{})
	c.admission = preview
	c.confirmations = AnalysisRunConfirmations{SecurityReview: confirmations.SecurityReview, ProviderIDs: append([]string(nil), confirmations.ProviderIDs...)}
	c.windowStart = time.Now()
	c.elapsedBase = c.run.ElapsedSeconds
	c.fileStopReason, c.featureStopReason = "", ""
	go s.runAnalysisWindow(ctx, c.run.Identity, c.done)
	return nil
}

func (s *Service) runAnalysisWindow(ctx context.Context, identity AnalysisRunIdentity, done chan struct{}) {
	c := s.analysisRun
	defer s.closeAnalysisWindow(done)
	defer s.analysisRun.cancel()
	c.mu.Lock()
	if c.run == nil || c.run.Identity != identity || c.fault != nil {
		c.mu.Unlock()
		return
	}
	if c.run.Status != AnalysisRunQueued {
		s.finishAnalysisWindowLocked(ctx)
		c.mu.Unlock()
		return
	}
	c.run.Status = AnalysisRunRunning
	featureDone := make(chan struct{})
	if c.run.Features != nil && analysisStageNeedsWork(c.run.Features.Status) {
		featureContext, cancelFeature := context.WithCancel(ctx)
		c.featureCancel = cancelFeature
		go s.runAnalysisFeatureWindow(featureContext, identity, featureDone)
	} else {
		close(featureDone)
	}
	budget := c.windowBudget
	c.mu.Unlock()
	fileContext, cancelFiles := context.WithTimeout(ctx, budget)
	s.runAnalysisFiles(fileContext, identity)
	cancelFiles()
	<-featureDone
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil || c.run.Identity != identity || c.fault != nil {
		return
	}
	if c.run.Status != AnalysisRunRunning || ctx.Err() != nil {
		s.finishAnalysisWindowLocked(ctx)
		return
	}
	if c.fileStopReason != "" || c.featureStopReason != "" {
		c.run.Status, c.run.Reason = AnalysisRunPaused, c.fileStopReason
		if c.run.Reason == "" {
			c.run.Reason = c.featureStopReason
		}
		_ = s.saveAnalysisRunLocked(false)
		return
	}
	s.finishCompletedAnalysisRunLocked(ctx)
}

func (c *analysisRunController) stageRequest(fileIndex, stageIndex int) analysisFileStageRequest {
	file := c.run.Files[fileIndex]
	stage := file.Stages[stageIndex]
	plan := c.admission.Files[fileIndex].Stages[stageIndex]
	return analysisFileStageRequest{
		Run: c.run.Identity, File: file.AnalysisFileIdentity, Stage: stage.Stage, Models: cloneAnalysisModels(c.run.Plan.Models),
		Refresh:               c.run.Plan.Refresh || ((c.run.Plan.RetryStaleFailed || c.run.Plan.RecoverIncomplete || c.run.Plan.StaleOnly) && !plan.Cached),
		RemainingAttempts:     min(plan.MaxModelRequests, c.run.Plan.Limits.MaxAttemptsPerStage-stage.Attempts),
		ConfirmRemoteProvider: analysisProviderConfirmed(c.confirmations, plan.ProviderID),
		SecurityReview:        c.confirmations.SecurityReview,
	}
}

func (s *Service) finishAnalysisWindowLocked(ctx context.Context) bool {
	c := s.analysisRun
	switch c.run.Status {
	case AnalysisRunStale, AnalysisRunCanceled:
		return true
	case AnalysisRunCanceling:
		c.run.Status = AnalysisRunCanceled
		interruptAnalysisStages(c.run, AnalysisStageCanceled)
	case AnalysisRunPausing:
		c.run.Status = AnalysisRunPaused
	case AnalysisRunRunning:
		if ctx.Err() == nil {
			return false
		}
		if errors.Is(context.Cause(ctx), context.Canceled) {
			c.run.Status = AnalysisRunCanceled
			c.run.Reason = "Analysis canceled by the user."
			interruptAnalysisStages(c.run, AnalysisStageCanceled)
		} else {
			c.run.Status = AnalysisRunPaused
			c.run.Reason = "The dispatch allowance ended; preview and resume the remaining work."
			interruptAnalysisStages(c.run, AnalysisStageInterrupted)
		}
	default:
		return true
	}
	_ = s.saveAnalysisRunLocked(false)
	return true
}

func (s *Service) analysisRunAuthority(fileIndex, stageIndex int) analysisFileStageAuthority {
	c := s.analysisRun

	return analysisFileStageAuthority{
		Check: func(ctx context.Context, identity AnalysisRunIdentity) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.run == nil || c.run.Identity != identity {
				return project.ErrRevisionConflict
			}
			return s.validateAnalysisStageAuthorityLocked(ctx, fileIndex, stageIndex, identity, c.run.Files[fileIndex].AnalysisFileIdentity, c.run.Files[fileIndex].Stages[stageIndex].Stage)
		},
		BeforeAttempt: func(ctx context.Context, identity AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if err := s.validateAnalysisStageAuthorityLocked(ctx, fileIndex, stageIndex, identity, file, stage); err != nil {
				return err
			}
			progress := &c.run.Files[fileIndex].Stages[stageIndex]
			if progress.Attempts >= c.run.Plan.Limits.MaxAttemptsPerStage {
				return errAnalysisAttemptBudget
			}
			progress.Attempts++
			return s.saveAnalysisRunLocked(false)
		},
		Publish: func(identity AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage, write func() error) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if err := s.validateAnalysisStageAuthorityLocked(context.Background(), fileIndex, stageIndex, identity, file, stage); err != nil {
				return err
			}
			if err := write(); err != nil {
				if errors.Is(err, project.ErrRevisionConflict) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				s.failAnalysisPersistenceLocked()
				return errAnalysisRunPersistence
			}
			return nil
		},
	}
}

func (s *Service) updateAnalysisElapsedLocked() {
	c := s.analysisRun
	if c.run == nil || c.windowStart.IsZero() {
		return
	}
	active := time.Since(c.windowStart)
	if c.run.Plan.Features == nil {
		active = min(active, c.windowBudget)
	}
	elapsed := int64(active / time.Second)
	c.run.WindowElapsedSeconds = elapsed
	c.run.ElapsedSeconds = c.elapsedBase + elapsed
}

func (s *Service) staleAnalysisRunLocked() {
	c := s.analysisRun
	c.run.Status = AnalysisRunStale
	c.run.Reason = "Project source, policy or provider identity changed; start a new analysis."
	interruptAnalysisStages(c.run, AnalysisStageStale)
	if c.cancel != nil {
		c.cancel()
	}
}

func (s *Service) interruptAnalysisRunForProjectChange() {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return
	}
	// Leaving or restoring a project revokes dispatch authority, not the
	// freshness of its evidence. Reads validate the captured source identity.
	switch c.run.Status {
	case AnalysisRunQueued, AnalysisRunRunning, AnalysisRunPausing:
		c.run.Status = AnalysisRunInterrupted
		c.run.Reason = "Analysis was interrupted; preview and resume explicitly."
		interruptAnalysisStages(c.run, AnalysisStageInterrupted)
	case AnalysisRunCanceling:
		c.run.Status = AnalysisRunCanceled
		interruptAnalysisStages(c.run, AnalysisStageCanceled)
	default:
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	_ = s.saveAnalysisRunLocked(false) // Retained fault blocks any further dispatch.
}

func (s *Service) closeAnalysisWindow(done chan struct{}) {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done == done {
		if c.cancel != nil {
			c.cancel()
		}
		c.cancel = nil
		c.featureCancel = nil
		c.done = nil
		c.admission = nil
		c.confirmations = AnalysisRunConfirmations{}
		c.windowStart = time.Time{}
	}
	close(done)
}

func (s *Service) recordAnalysisStageLocked(ctx context.Context, identity AnalysisRunIdentity, fileIndex, stageIndex int, result analysisFileStageResult, err error) bool {
	c := s.analysisRun
	if c.run == nil || c.run.Identity != identity || c.fault != nil {
		return false
	}
	if c.run.Status == AnalysisRunStale || c.run.Status == AnalysisRunCanceled || c.run.Status == AnalysisRunCanceling || c.run.Status == AnalysisRunInterrupted {
		s.finishAnalysisWindowLocked(ctx)
		return false
	}
	if err != nil {
		s.interruptAnalysisStageLocked(ctx, fileIndex, stageIndex, err)
		return false
	}

	recordAnalysisResult(c.run, fileIndex, stageIndex, result)
	if analysisFileFinished(c.run.Files[fileIndex]) {
		c.run.WindowFilesCompleted++
	}
	if err := s.saveAnalysisRunLocked(false); err != nil {
		return false
	}
	return true
}

func (s *Service) interruptAnalysisStageLocked(ctx context.Context, fileIndex, stageIndex int, err error) {
	c := s.analysisRun
	current := &c.run.Files[fileIndex].Stages[stageIndex]
	current.Status = AnalysisStageInterrupted
	current.Reason = "Analysis stage could not complete."
	if errors.Is(context.Cause(ctx), context.Canceled) {
		current.Status = AnalysisStageCanceled
		current.Reason = "Analysis canceled by the user."
	}
	if errors.Is(err, project.ErrRevisionConflict) {
		s.staleAnalysisRunLocked()
	} else if c.run.Status == AnalysisRunPausing || ctx.Err() != nil || errors.Is(err, errAnalysisAttemptBudget) {
		c.fileStopReason = "The file dispatch allowance ended; preview and resume the remaining work."
		if c.run.Status == AnalysisRunRunning {
			c.run.Reason = "File analysis reached its allowance. Feature discovery continues independently."
		}
	} else {
		c.run.Status = AnalysisRunInterrupted
		c.run.Reason = "Analysis stopped before the stage completed; review and resume."
		if c.cancel != nil {
			c.cancel()
		}
	}
	_ = s.saveAnalysisRunLocked(false)
}

func analysisProviderConfirmed(confirmations AnalysisRunConfirmations, providerID string) bool {
	for _, id := range confirmations.ProviderIDs {
		if id == providerID {
			return true
		}
	}
	return false
}

func (s *Service) validateAnalysisStageAuthorityLocked(ctx context.Context, fileIndex, stageIndex int, identity AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage) error {
	c := s.analysisRun
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.run == nil || c.run.Identity != identity || c.run.Files[fileIndex].AnalysisFileIdentity != file || c.run.Files[fileIndex].Stages[stageIndex].Stage != stage {
		return project.ErrRevisionConflict
	}
	if c.fault != nil {
		return errAnalysisRunPersistence
	}
	if c.run.Status != AnalysisRunRunning && c.run.Status != AnalysisRunPausing {
		return project.ErrRevisionConflict
	}
	return s.validateAnalysisQueue(ctx, c.root, &c.run.Plan, false)
}

func (s *Service) failExhaustedAnalysisStageLocked(fileIndex, stageIndex int) error {
	c := s.analysisRun
	stage := &c.run.Files[fileIndex].Stages[stageIndex]
	stage.Status = AnalysisStageFailed
	stage.Reason = "The stage exhausted its total attempt allowance."
	if analysisFileFinished(c.run.Files[fileIndex]) {
		c.run.WindowFilesCompleted++
	}
	return s.saveAnalysisRunLocked(false)
}

func (s *Service) finishCompletedAnalysisRunLocked(ctx context.Context) {
	c := s.analysisRun
	if err := s.validateAnalysisCompletedRun(ctx, c.root, &c.run.Plan); err != nil {
		s.staleAnalysisRunLocked()
	} else {
		refreshAnalysisSections(c.run)
		c.run.Status = analysisFinishedStatus(c.run)
	}
	_ = s.saveAnalysisRunLocked(false) // Failure is retained and stops this worker.
}

func (s *Service) validateAnalysisCompletedRun(ctx context.Context, root string, plan *AnalysisRunPreview) error {
	if err := s.validateAnalysisQueue(ctx, root, plan, false); err != nil {
		return err
	}
	index, err := s.manager.Index()
	if err != nil {
		return err
	}
	return project.VerifyProjectSources(ctx, root, index)
}
