package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/api"
	"github.com/nanaki-93/kasuri/v2/internal/app"
)

// SetupHandler serves project setup: the project's own configuration file.
type SetupHandler struct{ service *app.Service }

func NewSetupHandler(service *app.Service) *SetupHandler {
	return &SetupHandler{service: service}
}

type setupIdentity struct {
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
}

type setupModelTestRequest struct {
	setupIdentity
	Scope string         `json:"scope"`
	Model app.SetupModel `json:"model"`
}

func (h *SetupHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	value, err := h.service.ProjectSetup(r.Context(), query.Get("project_id"), query.Get("project_revision"))
	respondSetup(w, "project setup unavailable", value, err)
}

func (h *SetupHandler) Save(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var request app.ProjectSetupRequest
	if !decodeStrictJSON(w, r, &request, "invalid project setup", "Provide the current project identity, the three models, the agent roles and the budget.") {
		return
	}
	value, err := h.service.SaveProjectSetup(r.Context(), request)
	respondSetup(w, "project setup not saved", value, err)
}

func (h *SetupHandler) SaveTimeouts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var request app.ProjectTimeoutsRequest
	if !decodeStrictJSON(w, r, &request, "invalid time limits", "Provide the current project identity and every time limit in seconds.") {
		return
	}
	value, err := h.service.SaveProjectTimeouts(r.Context(), request)
	respondSetup(w, "time limits not saved", value, err)
}

func (h *SetupHandler) Skip(w http.ResponseWriter, r *http.Request) {
	var request setupIdentity
	if !decodeStrictJSON(w, r, &request, "invalid setup skip", "Provide the current project identity.") {
		return
	}
	value, err := h.service.SkipProjectSetup(r.Context(), request.ProjectID, request.ProjectRevision)
	respondSetup(w, "setup skip not recorded", value, err)
}

func (h *SetupHandler) TestModel(w http.ResponseWriter, r *http.Request) {
	var request setupModelTestRequest
	if !decodeStrictJSON(w, r, &request, "invalid model test", "Provide the current project identity, a scope and the model to test.") {
		return
	}
	value, err := h.service.TestSetupModel(r.Context(), request.ProjectID, request.ProjectRevision, request.Scope, request.Model)
	respondSetup(w, "model test failed", value, err)
}

func respondSetup(w http.ResponseWriter, action string, value any, err error) {
	switch {
	case err == nil:
		api.WriteJSON(w, http.StatusOK, value)
	case errors.Is(err, app.ErrSetupInvalid):
		api.WriteAppError(w, api.BadRequest(action, sentence(strings.TrimPrefix(err.Error(), app.ErrSetupInvalid.Error()+": ")), err))
	case errors.Is(err, app.ErrSetupBusy):
		api.WriteAppError(w, api.Conflict(action, sentence(strings.TrimPrefix(err.Error(), app.ErrSetupBusy.Error()+": ")), err))
	case writeContextError(w, action, err):
	default:
		writeProjectError(w, action, err)
	}
}

// sentence capitalizes a message and ends it with a period.
func sentence(text string) string {
	if text == "" {
		return text
	}
	text = strings.ToUpper(text[:1]) + text[1:]
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return text
}
