package handlers

import (
	"errors"
	"net/http"

	"github.com/nanaki-93/kasuri/v2/internal/api"
	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
)

type AgentSettingsHandler struct{ service *app.Service }

func NewAgentSettingsHandler(service *app.Service) *AgentSettingsHandler {
	return &AgentSettingsHandler{service: service}
}

func (h *AgentSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query, ok := analysisQuery(w, r, []string{"project_id", "project_revision"}, nil)
	if !ok {
		return
	}
	value, err := h.service.AgentSettings(r.Context(), app.AgentRunQuery{ProjectID: query.Get("project_id"), ProjectRevision: query.Get("project_revision")})
	respondAgentSettings(w, value, err)
}

func (h *AgentSettingsHandler) Control(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var request app.AgentSettingsControl
	if !decodeStrictJSON(w, r, &request, "invalid agent settings", "Provide the current project, configuration, version and a confirmed replace or reset action.") {
		return
	}
	value, err := h.service.ControlAgentSettings(r.Context(), request)
	respondAgentSettings(w, value, err)
}

func respondAgentSettings(w http.ResponseWriter, value any, err error) {
	switch {
	case errors.Is(err, agentstate.ErrCorrupt), errors.Is(err, agentstate.ErrPersistence):
		api.WriteAppError(w, api.Internal("agent settings unavailable", "Existing private data was preserved. Repair the storage error before editing settings or admitting new work.", err))
	case err != nil:
		if !writeContextError(w, "agent settings", err) {
			writeProjectError(w, "agent settings request failed", err)
		}
	default:
		api.WriteJSON(w, http.StatusOK, value)
	}
}
