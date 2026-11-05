package handlers

import (
	"errors"
	"net/http"

	"github.com/nanaki-93/kogin/v2/internal/api"
	"github.com/nanaki-93/kogin/v2/internal/app"
)

type profileRequest struct {
	ProjectRevision string             `json:"project_revision"`
	Profile         app.ProjectProfile `json:"profile"`
}

type healthTestsRequest struct {
	ProjectRevision string `json:"project_revision"`
}

// Profile returns the project's profile, or the default when none is saved.
func (h *ProjectHandler) Profile(w http.ResponseWriter, _ *http.Request) {
	profile, err := h.service.ProjectProfile()
	if err != nil {
		writeProjectError(w, "project profile unavailable", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, profile)
}

// SaveProfile stores the project's profile.
func (h *ProjectHandler) SaveProfile(w http.ResponseWriter, r *http.Request) {
	var request profileRequest
	if !decodeStrictJSON(w, r, &request, "invalid project profile request", "Provide project_revision and profile.") || !requireCurrentRevision(w, h.manager, request.ProjectRevision) {
		return
	}
	profile, err := h.service.SaveProjectProfile(request.Profile)
	if errors.Is(err, app.ErrInvalidProfile) {
		api.WriteAppError(w, api.BadRequest("invalid project profile", err.Error(), err))
		return
	}
	if err != nil {
		writeProjectError(w, "project profile not saved", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, profile)
}

// Health measures the project: finish line, tests, repeated and unused
// code, complexity, the trend and tasks that would improve it.
func (h *ProjectHandler) Health(w http.ResponseWriter, r *http.Request) {
	if !requireCurrentRevision(w, h.manager, r.URL.Query().Get("project_revision")) {
		return
	}
	health, err := h.service.ProjectHealth(r.Context())
	if err != nil {
		writeProjectError(w, "project health unavailable", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, health)
}

// RunHealthTests starts a run of the project's tests with coverage.
func (h *ProjectHandler) RunHealthTests(w http.ResponseWriter, r *http.Request) {
	var request healthTestsRequest
	if !decodeStrictJSON(w, r, &request, "invalid test run request", "Provide project_revision.") || !requireCurrentRevision(w, h.manager, request.ProjectRevision) {
		return
	}
	err := h.service.RunHealthTests(r.Context(), request.ProjectRevision)
	switch {
	case errors.Is(err, app.ErrHealthTestsBusy):
		api.WriteAppError(w, api.Conflict("tests already running", "The tests are already running. Wait for that run to finish.", err))
	case err != nil:
		api.WriteAppError(w, api.Forbidden("tests not started", "Trust local execution for this project (Settings) before running its tests.", err))
	default:
		api.WriteJSON(w, http.StatusAccepted, map[string]bool{"started": true})
	}
}
