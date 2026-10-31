package handlers

import (
	"net/http"

	"github.com/nanaki-93/mini-orca/v2/internal/api"
)

type executionTrustRequest struct {
	ProjectRevision string `json:"project_revision"`
	Confirm         bool   `json:"confirm"`
}

// ExecutionTrust reports the command scope that can execute imported project
// code and whether this active project revision has been explicitly trusted.
func (h *ProjectHandler) ExecutionTrust(w http.ResponseWriter, r *http.Request) {
	revision := r.URL.Query().Get("project_revision")
	if !requireCurrentRevision(w, h.manager, revision) {
		return
	}
	trust, err := h.service.ExecutionTrust(r.Context(), revision, r.URL.Query().Get("task_test_name"))
	if err != nil {
		writeProjectError(w, "execution trust lookup failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, trust)
}

// TrustProjectExecution saves or revokes deliberate consent for this project path.
func (h *ProjectHandler) TrustProjectExecution(w http.ResponseWriter, r *http.Request) {
	var request executionTrustRequest
	if !decodeStrictJSON(w, r, &request, "invalid execution trust request", "Provide project_revision and confirm: true.") || !requireCurrentRevision(w, h.manager, request.ProjectRevision) {
		return
	}
	trust, err := h.service.TrustProjectExecution(r.Context(), request.ProjectRevision, request.Confirm)
	if err != nil {
		api.WriteAppError(w, api.BadRequest("trusted local execution confirmation required", "Review the command scope and explicitly trust local execution for this project.", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, trust)
}
