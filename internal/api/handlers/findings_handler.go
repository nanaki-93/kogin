package handlers

import (
	"net/http"

	"github.com/nanaki-93/kogin/v2/internal/api"
	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

type findingStatusRequest struct {
	ProjectRevision string `json:"project_revision"`
	Status          string `json:"status"`
	Kind            string `json:"kind,omitempty"`
	Path            string `json:"path,omitempty"`
	Source          string `json:"source,omitempty"`
}

// Findings lists source-free verified reports and AI suggestions with their
// provenance, confidence, lifecycle, location, and freshness intact.
func (h *ProjectHandler) Findings(w http.ResponseWriter, r *http.Request) {
	if !requireCurrentRevision(w, h.manager, r.URL.Query().Get("project_revision")) {
		return
	}
	findings, err := h.service.ListFindings(app.FindingFilter{
		Source: r.URL.Query().Get("source"), Confidence: r.URL.Query().Get("confidence"),
		Severity: r.URL.Query().Get("severity"), Status: r.URL.Query().Get("status"), Freshness: r.URL.Query().Get("freshness"),
	})
	if err != nil {
		writeProjectError(w, "finding lookup failed", err)
		return
	}
	completed, err := h.service.FindingCompletions()
	if err != nil {
		writeProjectError(w, "finding completion lookup failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, struct {
		ProjectID       string                      `json:"project_id"`
		ProjectRevision string                      `json:"project_revision"`
		Findings        []project.UnifiedFinding    `json:"findings"`
		Completed       []project.FindingCompletion `json:"completed"`
	}{ProjectID: findingsProjectID(h.manager), ProjectRevision: r.URL.Query().Get("project_revision"), Findings: findings, Completed: completed})
}

// IssueTask drafts the task that fixes one issue, for the composer.
func (h *ProjectHandler) IssueTask(w http.ResponseWriter, r *http.Request) {
	if !requireCurrentRevision(w, h.manager, r.URL.Query().Get("project_revision")) {
		return
	}
	task, err := h.service.DraftIssueTask(r.PathValue("findingID"))
	if err != nil {
		writeProjectError(w, "issue task draft failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, task)
}

// IssuesTask drafts one task that fixes several open issues of one file
// (repeated id parameters); nothing is saved.
func (h *ProjectHandler) IssuesTask(w http.ResponseWriter, r *http.Request) {
	if !requireCurrentRevision(w, h.manager, r.URL.Query().Get("project_revision")) {
		return
	}
	task, err := h.service.DraftIssuesTask(r.URL.Query()["id"])
	if err != nil {
		writeProjectError(w, "issues task draft failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, task)
}

// UpdateFindingStatus changes only user triage for one deterministic finding.
func (h *ProjectHandler) UpdateFindingStatus(w http.ResponseWriter, r *http.Request) {
	var request findingStatusRequest
	if !decodeStrictJSON(w, r, &request, "invalid finding triage request", "Provide project_revision and status.") || !requireCurrentRevision(w, h.manager, request.ProjectRevision) {
		return
	}
	id := r.PathValue("findingID")
	err := h.service.UpdateFindingStatus(request.ProjectRevision, id, request.Status)
	if request.Kind != "" {
		err = h.service.UpdateFindingTargetStatus(request.ProjectRevision, project.FindingTarget{
			ID:     id,
			Kind:   request.Kind,
			Path:   request.Path,
			Source: request.Source,
		}, request.Status)
	}
	if err != nil {
		writeProjectError(w, "update finding triage failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func findingsProjectID(manager *project.Manager) string {
	analysis, err := manager.Analysis()
	if err != nil {
		return ""
	}
	return analysis.ProjectID
}
