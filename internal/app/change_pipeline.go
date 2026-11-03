package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
)

func workflowStageScope(stage int) config.ModelScope {
	switch stage {
	case 0:
		return config.BugModelScope
	case 1:
		return config.CodeModelScope
	default:
		return config.AnalyzeModelScope
	}
}

func (s *Service) workflowStageRuntime(stage int, profile string) modelRuntime {
	runtime, _ := s.selectedRuntime(workflowStageScope(stage), profile)
	runtime.effective.Timeout = max(s.conf().workflowModelTimeout, duration(s.conf().runtimes.function.effective.Timeout), duration(runtime.effective.Timeout)).String()
	return runtime
}

// StartChangeWorkflow captures consent and proposal identity before dispatch.
// The HTTP request only admits the worker; polling/Cancel use its persisted ID.
func (s *Service) StartChangeWorkflow(ctx context.Context, id string, request ChangeWorkflowRequest) (*ChangeSession, error) {
	ctx, err := s.freezeAgentSettings(ctx, s.manager.Root())
	if err != nil {
		return nil, err
	}
	request.Models = s.resolveWorkflowModels(ctx, request.Models)
	if err := request.Models.validate(); err != nil {
		return nil, err
	}
	if len(request.Message) > 8192 || strings.TrimSpace(request.Message) == "" && !request.IncludeComments {
		return nil, fmt.Errorf("provide a workflow request of at most 8192 bytes")
	}
	if err := s.validateModelSelections(ctx, request.Models.selections()); err != nil {
		return nil, err
	}
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	cleanedMessages := stripWorkflowTestMessages(session.Messages)
	start, queued, err := s.admitOrQueue(ctx, root, session, request, cleanedMessages)
	if err != nil || queued != nil {
		return queued, err
	}
	message, sent, err := workflowRequestMessage(session, request)
	if err != nil {
		return nil, err
	}
	agentCtx, release, err := s.newChangeWorkflowContext(ctx, request.Models)
	if err != nil {
		return nil, err
	}
	markCommentsSent(session.Comments, sent, session.Revision)
	request.Message = message
	retryMessage := workflowRetryMessage(request.Message, session.Workflow, session.Checks)
	w := s.prepareWorkflowRun(session, request, start, cleanedMessages)
	if err := storeChangeSession(root, session); err != nil {
		release()
		return nil, err
	}
	if start != startReview {
		delete(s.changeAuthority, session.ProjectID+"/"+id)
	}
	steps := 0
	if start.firstModelStage() == 0 {
		steps = pendingIssueSteps(session)
	}
	s.launchChangeWorkflow(agentCtx, release, root, id, w, request, start, retryMessage, steps)
	return session, nil
}

// admitOrQueue admits the run and returns where it starts, or records it as
// waiting when another task is changing its code and returns the task then.
// Caller holds changesMu.
func (s *Service) admitOrQueue(ctx context.Context, root string, session *ChangeSession, request ChangeWorkflowRequest, cleanedMessages []ChangeMessage) (workflowStart, *ChangeSession, error) {
	start, err := s.admitWorkflowSession(ctx, session, request, len(cleanedMessages))
	if err != nil {
		return start, nil, err
	}
	if err := s.requireMainUnchanged(ctx, root, session); err != nil {
		return start, nil, err
	}
	wait, err := s.blockingTask(ctx, session.ID, codeFiles(session, rules.NewResolver(root)))
	if err != nil || wait == nil {
		return start, nil, err
	}
	queued, err := s.queueChangeWorkflow(root, session, request, start, cleanedMessages, wait)
	return start, queued, err
}

// queueChangeWorkflow records a run that waits for another task before it
// starts. Caller holds changesMu.
func (s *Service) queueChangeWorkflow(root string, session *ChangeSession, request ChangeWorkflowRequest, start workflowStart, cleanedMessages []ChangeMessage, wait *ChangeWorkflowWait) (*ChangeSession, error) {
	w := s.prepareWorkflowRun(session, request, start, cleanedMessages)
	w.Status, w.Reason, w.Waiting, w.Pending = "waiting", waitReason(wait), wait, pendingRun(request)
	stopChangeWorkflowStages(w)
	if err := storeChangeSession(root, session); err != nil {
		return nil, err
	}
	s.notifyWorkflow(context.Background(), session, "")
	return session, nil
}

// admitWorkflowSession checks the session may start this run and returns
// where it starts.
func (s *Service) admitWorkflowSession(ctx context.Context, session *ChangeSession, request ChangeWorkflowRequest, messages int) (workflowStart, error) {
	if err := s.admitChangeWorkflow(ctx, session, request, messages); err != nil {
		return workflowStart{}, err
	}
	if err := approveWorkflowPlan(session, request.PlanHash); err != nil {
		return workflowStart{}, err
	}
	start, err := s.workflowStartFor(session, request.ResumeFrom)
	if err != nil {
		return workflowStart{}, err
	}
	if start == startChecks && request.IncludeComments {
		// They would be marked sent although no agent writes code.
		return workflowStart{}, fmt.Errorf("a checks start writes no code; send line comments with a run that writes the change or its tests")
	}
	return start, validateResumedModels(session, request.Models, start.kept)
}

// workflowRequestMessage is the run's request with any line-comment brief.
func workflowRequestMessage(session *ChangeSession, request ChangeWorkflowRequest) (string, []string, error) {
	message, sent, err := withRepairBrief(session, request.Message, request.IncludeComments)
	if err == nil && len(message) > maxWorkflowRetryMessage {
		err = fmt.Errorf("the request and its line comments exceed %d bytes; shorten the request", maxWorkflowRetryMessage)
	}
	return message, sent, err
}

// launchChangeWorkflow starts the admitted run's worker under its total
// deadline, which grows with the issue steps it will run.
func (s *Service) launchChangeWorkflow(agentCtx context.Context, release context.CancelFunc, root, sessionID string, w *ChangeWorkflow, request ChangeWorkflowRequest, start workflowStart, message string, steps int) {
	budget := s.changeWorkflowTimeout(agentCtx, request.Models, start.firstModelStage(), request.AutoRepair) + s.issueStepsBudget(agentCtx, request.Models, steps)
	workCtx, cancelTimeout := context.WithTimeoutCause(agentCtx, budget, &workflowTimeoutError{limit: budget})
	cancel := func() { cancelTimeout(); release() }
	workCtx, interrupt := context.WithCancelCause(workCtx)
	workCtx = context.WithValue(workCtx, changeWorkflowContextKey{}, w.ID)
	if request.StreamOutput {
		workCtx = agentruntime.WithStreaming(workCtx)
	}
	job := &changeWorkflowJob{root: root, sessionID: sessionID, id: w.ID, models: request.Models, specialists: workflowSpecialistIDs(w), autoRepair: request.AutoRepair, request: request, start: start, cancel: cancel, interrupt: interrupt, done: make(chan struct{})}
	s.changeWorkflow.mu.Lock()
	s.changeWorkflow.setJob(job)
	s.changeWorkflow.mu.Unlock()
	go s.runChangeWorkflow(workCtx, job, message)
}

func (s *Service) newChangeWorkflowContext(ctx context.Context, models ChangeWorkflowModels) (context.Context, context.CancelFunc, error) {
	// Jobs capture settings, without inheriting a request's budget or policy.
	background := context.WithValue(context.Background(), agentSettingsKey{}, ctx.Value(agentSettingsKey{}))
	return s.workflowAgentContext(background, models)
}

func (s *Service) admitChangeWorkflow(ctx context.Context, session *ChangeSession, request ChangeWorkflowRequest, messageCount int) error {
	s.changeWorkflow.mu.Lock()
	existing := s.changeWorkflow.getJob(session.ID)
	busy := existing != nil && !existing.finished
	s.changeWorkflow.mu.Unlock()
	if busy {
		return fmt.Errorf("%w: a change workflow is already active", project.ErrRevisionConflict)
	}
	if session.Kind == "instructions" || messageCount > maxChangeMessages-4 {
		return fmt.Errorf("start a new feature, fix, performance or security conversation")
	}
	if session.Kind == "security" && !request.ConfirmSecurity {
		return fmt.Errorf("security workflow requires explicit review intent")
	}
	// A testing agent with tools creates the test files it needs; a one-shot
	// one writes only the test files the task holds.
	testRuntime := s.workflowStageRuntime(1, request.Models.Test)
	hasTestTarget := toolStageAvailable(session, testRuntime)
	for _, target := range session.Targets {
		hasTestTarget = hasTestTarget || isTestTarget(target)
	}
	if !hasTestTarget {
		return fmt.Errorf("include a test path (e.g. _test.go, .test.ts, test_*.py) in the captured scope for the testing agent")
	}
	for i, profile := range request.Models.selections() {
		runtime := s.workflowStageRuntime(i, profile)
		if request.StreamOutput && !llm.ProviderCapabilities(runtime.profile).Streaming {
			return fmt.Errorf("selected model %s does not support streaming output", profile)
		}
		if err := requireModelRuntimeConfirmation(runtime, slices.Contains(request.ConfirmedProfiles, profile)); err != nil {
			return err
		}
	}
	return s.requireChangeExecutionTrust(ctx, session)
}

func (s *Service) runChangeWorkflow(ctx context.Context, job *changeWorkflowJob, message string) {
	defer job.cancel()
	defer close(job.done)
	defer s.lockTaskWorktree(job)()
	currentMessage := message
	if job.start == startCreate {
		currentMessage = s.probeUncheckedChangesBeforeWorkflow(ctx, job, message)
	}
	limit := 0
	if job.autoRepair {
		limit = s.admittedRuntime(ctx).Limits.Repairs
	}
	s.changesMu.Lock()
	session, runErr := loadChangeSession(job.root, job.sessionID)
	var input agentruntime.RepairGraphInput
	if runErr == nil {
		input = agentruntime.RepairGraphInput{Snapshot: changeGraphSnapshot(session), MaxRepairs: limit}
	}
	s.changesMu.Unlock()
	if runErr == nil {
		runErr = agentruntime.RunRepairGraph(ctx, input, func(ctx context.Context, attempt int) (bool, error) {
			if attempt > 0 {
				nextMsg, err := s.prepareWorkflowAutoRepair(ctx, job, message, attempt)
				if err != nil {
					return false, err
				}
				currentMessage = nextMsg
			}
			start := startCreate // A repair always writes the change again.
			if attempt == 0 {
				start = job.start
			}
			attemptErr := s.executeChangeGraph(ctx, job, start.node, currentMessage)
			if attempt < limit {
				repair, err := s.shouldAutoRepairWorkflow(ctx, job, attemptErr)
				if err != nil || repair {
					return repair, err
				}
			}
			return false, attemptErr
		})
	}
	s.finishChangeWorkflow(ctx, job, runErr)
}

func (s *Service) probeUncheckedChangesBeforeWorkflow(ctx context.Context, job *changeWorkflowJob, message string) string {
	if strings.Contains(message, "Previous check failures to resolve:") || strings.Contains(message, "Critic review feedback:") {
		return message
	}
	s.changesMu.Lock()
	session, err := loadChangeSession(job.root, job.sessionID)
	needsProbe := err == nil && session.Workflow != nil && session.Workflow.ID == job.id && len(session.Changes) > 0 && len(session.Checks) == 0
	var id ChangeIdentity
	if needsProbe {
		id = identityForChange(session)
	}
	s.changesMu.Unlock()
	if !needsProbe {
		return message
	}
	checked, err := s.CheckChange(ctx, job.sessionID, ChangeCheckRequest{ChangeIdentity: id, CheckOptions: CheckOptions{RunTests: true}})
	if err != nil || requiredChecksPassed(checked.Checks) {
		return message
	}
	return workflowRetryMessage(message, checked.Workflow, checked.Checks)
}

var errWorkflowChecksFailed = errors.New("workflow checks failed")

func identityForChange(session *ChangeSession) ChangeIdentity {
	return ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision, Hash: session.Hash}
}

func (s *Service) beginChangeWorkflowStage(ctx context.Context, job *changeWorkflowJob, stage int, identity ChangeIdentity) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	snapshot := agentruntime.ChangeSnapshot{ProjectID: identity.ProjectID, ProjectRevision: identity.ProjectRevision, SessionID: job.sessionID, WorkflowID: job.id, Revision: identity.Revision, Hash: identity.Hash}
	session, err := s.loadChangeGraphSnapshot(ctx, job, snapshot)
	if err != nil {
		return nil, err
	}
	chargeWorkflowStage(ctx, job, session.Workflow)
	if stage > 0 {
		completeWorkflowStage(&session.Workflow.Stages[stage-1], time.Now().UTC())
	}
	// The stage would build on what the main branch has since replaced: the
	// run stops before it, the stage before it complete.
	if err := s.requireMainUnchanged(ctx, job.root, session); err != nil {
		if storeErr := storeChangeSession(job.root, session); storeErr != nil {
			return nil, storeErr
		}
		return nil, err
	}
	session.Workflow.Stages[stage].StartedAt = time.Now().UTC()
	session.Workflow.Stages[stage].Status = "running"
	session.Workflow.Stages[stage].Reason = ""
	session.Workflow.UpdatedAt, session.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	err = storeChangeSession(job.root, session)
	s.notifyWorkflow(ctx, session, session.Workflow.Stages[stage].Name)
	return session, err
}

func (s *Service) finishChangeWorkflow(ctx context.Context, job *changeWorkflowJob, runErr error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, err := loadChangeSession(job.root, job.sessionID)
	if err == nil && session.Workflow != nil && session.Workflow.ID == job.id {
		w := session.Workflow
		chargeWorkflowStage(ctx, job, w)
		finishWorkflowStageTiming(w, time.Now().UTC())
		if runErr == nil {
			runErr = s.verifyChangeCurrent(ctx, job.root, session)
		}
		runErr = workflowDeadlineCause(ctx, runErr)
		var limit *llm.PlanLimitError
		if runErr == nil {
			w.Stages[2].Status = "completed"
			w.Status = "changes_requested"
			if w.Review != nil && w.Review.Verdict == "approve" {
				w.Status, w.Stages[3].Status = "awaiting_human_review", "waiting"
			}
		} else {
			w.Status, w.Reason = workflowFailure(runErr, session.Checks)
			if errors.As(runErr, &limit) {
				s.pauseChangeWorkflow(session, limit)
			}
			var wait *taskWaitError
			if errors.As(runErr, &wait) {
				w.Waiting, w.Pending = wait.wait, pendingRun(job.request)
			}
			stopChangeWorkflowStages(w)
			if !errors.Is(runErr, errWorkflowChecksFailed) && !s.hasLivePassingChecks(session) {
				delete(s.changeAuthority, session.ProjectID+"/"+session.ID)
			}
		}
		w.UpdatedAt, session.UpdatedAt = time.Now().UTC(), time.Now().UTC()
		err = storeChangeSession(job.root, session)
	}
	s.changeWorkflow.mu.Lock()
	defer s.changeWorkflow.mu.Unlock()
	job.finished = true
	if err != nil {
		job.failure = "Workflow progress could not be saved. Restore the conversation and run again."
	}
	if session != nil {
		s.notifyWorkflow(ctx, session, "")
	} else {
		s.notifyEvent("workflow")
	}
}

// pauseChangeWorkflow keeps a run that stopped at a plan's usage limit
// resumable from the stage that reached it.
func (s *Service) pauseChangeWorkflow(session *ChangeSession, limit *llm.PlanLimitError) {
	w := session.Workflow
	stage := "create"
	for _, candidate := range w.Stages[:3] {
		if candidate.Status == "running" {
			stage = candidate.Name
			break
		}
	}
	resume := stage
	switch {
	case stage == "test" && len(session.Changes) == 0:
		resume = "create"
	case stage == "review" && !s.hasLivePassingChecks(session):
		resume = "checks"
	}
	w.Status = "paused"
	w.Reason = fmt.Sprintf("The %s reached its usage limit during the %s stage. Nothing was lost: resume when the limit resets, or choose another model for that stage.", limit.Plan(), workflowStageLabel(stage))
	w.Pause = &ChangeWorkflowPause{Stage: stage, ResumeFrom: resume, Provider: string(limit.Provider)}
	if !limit.ResetsAt.IsZero() {
		w.Pause.ResumesAt = limit.ResetsAt.UTC()
	}
}
