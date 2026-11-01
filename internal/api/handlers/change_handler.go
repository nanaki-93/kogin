package handlers

import (
	"net/http"

	"github.com/nanaki-93/kasuri/v2/internal/api"
	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

type ChangeHandler struct {
	service *app.Service
	manager *project.Manager
}

func NewChangeHandler(service *app.Service, manager *project.Manager) *ChangeHandler {
	return &ChangeHandler{service, manager}
}

func decodeWorkflowBody(w http.ResponseWriter, r *http.Request, body any) bool {
	if err := api.DecodeJSON(w, r, body); err != nil {
		api.WriteRequestError(w, err, "invalid AI workflow request", "Provide the displayed task identity and supported fields.")
		return false
	}
	return true
}

func respondWorkflow(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeWorkflowError(w, "AI workflow request failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, value)
}

func guardedWorkflowRead(w http.ResponseWriter, r *http.Request, manager *project.Manager) bool {
	index, err := manager.Index()
	if err != nil {
		writeWorkflowError(w, "project unavailable", err)
		return false
	}
	if r.URL.Query().Get("project_id") != index.ProjectID || r.URL.Query().Get("project_revision") != index.ProjectRevision {
		writeWorkflowError(w, "project changed", project.ErrRevisionConflict)
		return false
	}
	return true
}

func (h *ChangeHandler) List(w http.ResponseWriter, r *http.Request) {
	if !guardedWorkflowRead(w, r, h.manager) {
		return
	}
	value, err := h.service.ChangeHistory(r.Context())
	respondWorkflow(w, value, err)
}

// Quarantine lists the records set aside or skipped in the selected checkout.
func (h *ChangeHandler) Quarantine(w http.ResponseWriter, r *http.Request) {
	if !guardedWorkflowRead(w, r, h.manager) {
		return
	}
	value, err := h.service.Quarantine(r.Context())
	respondWorkflow(w, value, err)
}

// Usage totals what agents spent per task in the selected checkout.
func (h *ChangeHandler) Usage(w http.ResponseWriter, r *http.Request) {
	if !guardedWorkflowRead(w, r, h.manager) {
		return
	}
	value, err := h.service.ChangeUsage(r.Context())
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !guardedWorkflowRead(w, r, h.manager) {
		return
	}
	value, err := h.service.ChangeSession(r.Context(), r.PathValue("sessionID"))
	respondWorkflow(w, value, err)
}

// Open captures a task in this checkout or, with worktree, in a new branch and
// worktree created for it.
func (h *ChangeHandler) Open(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeOpenRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	if request.Worktree {
		value, err := h.service.OpenChangeInWorktree(r.Context(), request)
		respondWorkflow(w, value, err)
		return
	}
	value, err := h.service.OpenChangeSession(r.Context(), request.ChangeCreateRequest)
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) Message(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeMessageRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	var value *app.ChangeSession
	var err error
	if request.Repair {
		value, err = h.service.RepairChangeMessage(r.Context(), r.PathValue("sessionID"), request)
	} else {
		value, err = h.service.SendChangeMessage(r.Context(), r.PathValue("sessionID"), request)
	}
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) Checks(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeCheckRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.CheckChange(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) Review(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeReviewRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.ReviewChange(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) Comment(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeCommentRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.CommentChangeLine(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeCommentDeleteRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.DeleteChangeComment(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}

// Delete removes a task that is not running and whose Apply cannot be undone.
func (h *ChangeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeIdentity
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	id := r.PathValue("sessionID")
	if err := h.service.DeleteChange(r.Context(), id, request); err != nil {
		writeWorkflowError(w, "task deletion failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// Reanalyze re-checks a stale task's issue against the current files: a task
// still valid is captured again, one that is not is deleted.
func (h *ChangeHandler) Reanalyze(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeReanalysisRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.ReanalyzeChange(r.Context(), r.PathValue("sessionID"), request)
	if err != nil {
		writeWorkflowError(w, "task re-analysis failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, value)
}

func (h *ChangeHandler) Retake(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeRetakeRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.RetakeChange(r.Context(), r.PathValue("sessionID"), request)
	if err != nil {
		writeWorkflowError(w, "task retake failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, value)
}

func (h *ChangeHandler) Apply(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeApplyRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.ApplyChange(r.Context(), r.PathValue("sessionID"), request)
	if request.CommitMessage != "" || request.Head != "" {
		respondGit(w, value, err)
		return
	}
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) Undo(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeApplyRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.UndoChange(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) Recovery(w http.ResponseWriter, r *http.Request) {
	if !guardedWorkflowRead(w, r, h.manager) {
		return
	}
	value, err := h.service.ChangeRecovery()
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) ReviewFile(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeFileReviewRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.ReviewChangeFile(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}
