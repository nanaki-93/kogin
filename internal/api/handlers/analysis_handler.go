package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/api"
	"github.com/nanaki-93/kogin/v2/internal/app"
)

// AnalysisHandler exposes one project run and read-only categorized evidence.
type AnalysisHandler struct{ service *app.Service }

func NewAnalysisHandler(service *app.Service) *AnalysisHandler {
	return &AnalysisHandler{service: service}
}

func (h *AnalysisHandler) Preview(w http.ResponseWriter, r *http.Request) {
	var request app.AnalysisPreviewRequest
	if !decodeStrictJSON(w, r, &request, "invalid analysis preview", "Provide the project identity, project scope and bounded limits.") || !validAnalysisRequest(w, request.Validate()) {
		return
	}
	preview, err := h.service.PreviewAnalysisRun(r.Context(), request)
	if err != nil {
		writeProjectError(w, "analysis preview failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, preview)
}
func (h *AnalysisHandler) Start(w http.ResponseWriter, r *http.Request) {
	var request app.AnalysisRunStartRequest
	if !decodeStrictJSON(w, r, &request, "invalid analysis start", "Echo the current preview identity, limits and fresh confirmations.") || !validAnalysisRequest(w, request.Validate()) {
		return
	}
	run, err := h.service.StartAnalysisRun(r.Context(), request)
	if err != nil {
		writeProjectError(w, "analysis start failed", err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, run)
}
func (h *AnalysisHandler) Current(w http.ResponseWriter, r *http.Request) {
	query, ok := analysisQuery(w, r, []string{"project_id", "project_revision"}, nil)
	if !ok {
		return
	}
	run, err := h.service.ReadAnalysisRun(r.Context(), query.Get("project_id"), query.Get("project_revision"))
	if err != nil {
		writeProjectError(w, "analysis progress unavailable", err)
		return
	}
	if run == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	api.WriteJSON(w, http.StatusOK, run)
}
func (h *AnalysisHandler) Control(w http.ResponseWriter, r *http.Request) {
	var request app.AnalysisRunControlRequest
	if !decodeStrictJSON(w, r, &request, "invalid analysis control", "Provide the exact run identity and an explicit control action.") || !validAnalysisRequest(w, request.Validate()) {
		return
	}
	run, err := h.service.ControlAnalysisRun(r.Context(), request)
	if err != nil {
		writeProjectError(w, "analysis control failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, run)
}

func validAnalysisRequest(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	api.WriteAppError(w, api.BadRequest("invalid analysis request", "Provide complete identities, supported actions and limits within the documented bounds.", err))
	return false
}

// Reject duplicate or unknown guards rather than selecting an arbitrary value.
func analysisQuery(w http.ResponseWriter, r *http.Request, required, optional []string) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	allowed := map[string]bool{}
	for _, name := range append(required, optional...) {
		allowed[name] = true
	}
	valid := err == nil
	for name, values := range query {
		valid = valid && allowed[name] && len(values) == 1 && len(values[0]) > 0 && len(values[0]) <= 4096 && strings.TrimSpace(values[0]) == values[0]
	}
	for _, name := range required {
		valid = valid && query.Get(name) != ""
	}
	if !valid {
		api.WriteAppError(w, api.BadRequest("invalid analysis query", "Provide each required identity guard once; use only documented filters.", err))
		return nil, false
	}
	return query, true
}

func (h *AnalysisHandler) Selection(w http.ResponseWriter, r *http.Request) {
	query, ok := analysisQuery(w, r, []string{"project_id", "project_revision"}, nil)
	if !ok {
		return
	}
	selection, err := h.service.ReadAnalysisSelection(r.Context(), query.Get("project_id"), query.Get("project_revision"))
	if err != nil {
		writeProjectError(w, "analysis selection unavailable", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, selection)
}

func (h *AnalysisHandler) SaveSelection(w http.ResponseWriter, r *http.Request) {
	var request app.AnalysisSelectionRequest
	if !decodeStrictJSON(w, r, &request, "invalid analysis selection", "Provide project and selection identities and excluded_paths.") || !validAnalysisRequest(w, request.Validate()) {
		return
	}
	selection, err := h.service.SaveAnalysisSelection(r.Context(), request)
	if err != nil {
		writeProjectError(w, "analysis selection could not be saved", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, selection)
}
