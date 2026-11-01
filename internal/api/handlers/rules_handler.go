package handlers

import (
	"net/http"

	"github.com/nanaki-93/kasuri/v2/internal/api"
)

// Rules returns the project's test file patterns and the AGENTS.md files
// that declare code rules, so the Web UI classifies files as the daemon does.
func (h *ProjectHandler) Rules(w http.ResponseWriter, _ *http.Request) {
	rules, err := h.service.ProjectCodeRules()
	if err != nil {
		writeProjectError(w, "code rules unavailable", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, rules)
}

// Rules measures a task's files against their budgets and import rules.
func (h *ChangeHandler) Rules(w http.ResponseWriter, r *http.Request) {
	if !guardedWorkflowRead(w, r, h.manager) {
		return
	}
	value, err := h.service.ChangeCodeRules(r.Context(), r.PathValue("sessionID"))
	respondWorkflow(w, value, err)
}
