package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/api"
	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

type ProjectHandler struct {
	manager *project.Manager
	service *app.Service
}

type projectImportRequest struct {
	ProjectPath           string `json:"project_path"`
	ConfirmRemoteProvider bool   `json:"confirm_remote_provider,omitempty"`
}

type projectRestoreRequest struct {
	ProjectPath string `json:"project_path"`
}

type reindexRequest struct {
	ProjectRevision string `json:"project_revision"`
}

func NewProjectHandler(manager *project.Manager, service *app.Service) *ProjectHandler {
	return &ProjectHandler{manager: manager, service: service}
}

func (h *ProjectHandler) Import(w http.ResponseWriter, r *http.Request) {
	var request projectImportRequest
	if err := api.DecodeJSON(w, r, &request); err != nil {
		api.WriteRequestError(w, err, "invalid project import request", "Provide a JSON project_path.")
		return
	}
	analysis, err := h.service.ImportProject(r.Context(), request.ProjectPath, request.ConfirmRemoteProvider)
	if err != nil {
		if writeContextError(w, "project import", err) {
			return
		}
		api.WriteAppError(w, api.BadRequest("project import failed", "The selected project could not be imported.", err))
		return
	}
	if err := h.service.ActivateProject(analysis.Path, analysis); err != nil {
		api.WriteAppError(w, api.Internal("project activation failed", "The analysis was created but the project could not be activated.", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, analysis)
}

func (h *ProjectHandler) Restore(w http.ResponseWriter, r *http.Request) {
	var request projectRestoreRequest
	if err := api.DecodeJSON(w, r, &request); err != nil {
		api.WriteRequestError(w, err, "invalid project restore request", "Provide a JSON project_path.")
		return
	}
	analysis, err := h.service.RestoreProject(request.ProjectPath)
	if err != nil {
		api.WriteAppError(w, api.BadRequest("project restore failed", "The selected project could not be restored.", err))
		return
	}
	if err := h.service.RestoreActiveProject(analysis.Path, analysis); err != nil {
		api.WriteAppError(w, api.Internal("project restoration failed", "The stored analysis was loaded but the project could not be activated.", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, analysis)
}

// Overview combines source-free deterministic metrics with optional model and
// scan state for the project-level workspaces.
func (h *ProjectHandler) Overview(w http.ResponseWriter, r *http.Request) {
	if !requireCurrentRevision(w, h.manager, r.URL.Query().Get("project_revision")) {
		return
	}
	overview, err := h.service.ProjectOverview()
	if err != nil {
		writeProjectError(w, "project overview failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, overview)
}

// DocsComponent lists one docs component's code files with the first
// sentence of each one's saved summary.
func (h *ProjectHandler) DocsComponent(w http.ResponseWriter, r *http.Request) {
	if !requireCurrentRevision(w, h.manager, r.URL.Query().Get("project_revision")) {
		return
	}
	files, err := h.service.DocsComponentFiles(r.URL.Query().Get("path"))
	if err != nil {
		writeProjectError(w, "docs component failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, files)
}

// RefreshDocs starts writing the docs' model-written parts again: the
// summaries of components whose files changed, then the project overview.
func (h *ProjectHandler) RefreshDocs(w http.ResponseWriter, r *http.Request) {
	var request app.DocsRefreshRequest
	if !decodeStrictJSON(w, r, &request, "invalid docs refresh request", "Provide the current project identity.") {
		return
	}
	refresh, err := h.service.StartDocsRefresh(r.Context(), request)
	if err != nil {
		writeProjectError(w, "docs refresh failed", err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, refresh)
}

// Ask answers a question about the code with the Review role's agent and
// lines Kogin checked; nothing is written.
func (h *ProjectHandler) Ask(w http.ResponseWriter, r *http.Request) {
	var request app.AskRequest
	if !decodeStrictJSON(w, r, &request, "invalid question", "Provide the current project identity and a question.") {
		return
	}
	answer, err := h.service.AskCodebase(r.Context(), request)
	if err != nil {
		if writeContextError(w, "ask", err) {
			return
		}
		if errors.Is(err, app.ErrAskNeedsAgentModel) {
			api.WriteAppError(w, api.NewAppError(api.ErrorBadRequest, "ask failed", "Choose a Claude Code, Agy or Pi model for the Review role in Settings → Models & agents.", http.StatusUnprocessableEntity, err))
			return
		}
		writeProjectError(w, "ask failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, answer)
}

func (h *ProjectHandler) FileInfo(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if _, err := h.manager.IndexedFile(path); err != nil {
		writeProjectError(w, "file information failed", err)
		return
	}
	info, err := project.GetFileInfo(h.manager.Root(), path)
	if err != nil {
		api.WriteAppError(w, api.BadRequest("file information failed", "Choose an eligible project file and try again.", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, info)
}

// Index returns deterministic facts for the active project. The index contains
// only context-policy-eligible, project-relative paths and never source text.
func (h *ProjectHandler) Index(w http.ResponseWriter, _ *http.Request) {
	index, err := h.manager.Index()
	if err != nil {
		writeProjectError(w, "project index unavailable", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, index)
}

// Reindex refreshes deterministic facts without contacting the model.
func (h *ProjectHandler) Reindex(w http.ResponseWriter, r *http.Request) {
	var request reindexRequest
	if err := api.DecodeJSON(w, r, &request); err != nil {
		api.WriteRequestError(w, err, "invalid reindex request", "Provide project_revision.")
		return
	}
	if !requireCurrentRevision(w, h.manager, request.ProjectRevision) {
		return
	}
	var (
		index *project.ProjectIndex
		err   error
	)
	if h.service != nil {
		index, err = h.service.Reindex()
	} else {
		index, err = h.manager.Reindex()
	}
	if err != nil {
		writeProjectError(w, "project reindex failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, index)
}

// Events streams Server-Sent Events (SSE) when analysis runs, scans, or
// change workflows transition state for the active project.
func (h *ProjectHandler) Events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		api.WriteError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch, unsubscribe := h.service.SubscribeEvents()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("event: ready\ndata: {\"status\":\"connected\"}\n\n"))
	flusher.Flush()

	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.URL.Query().Get("once") == "true" {
		return
	}

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_, _ = w.Write([]byte("event: update\ndata: " + string(data) + "\n\n"))
			flusher.Flush()
		case <-ticker.C:
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			flusher.Flush()
		}
	}
}

func writeContextError(w http.ResponseWriter, action string, err error) bool {
	switch {
	case errors.Is(err, context.Canceled):
		api.WriteError(w, http.StatusRequestTimeout, action+" canceled")
	case errors.Is(err, context.DeadlineExceeded):
		api.WriteError(w, http.StatusGatewayTimeout, action+" timed out")
	default:
		return false
	}
	return true
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, value any, message, userMessage string) bool {
	if err := api.DecodeJSON(w, r, value); err != nil {
		api.WriteRequestError(w, err, message, userMessage)
		return false
	}
	return true
}

const agentSettingsStaleMessage = "The agent settings saved in Settings → Models & agents belong to an earlier model configuration. Reset them there, or save setup, to continue."

func writeProjectError(w http.ResponseWriter, action string, err error) {
	switch {
	case errors.Is(err, app.ErrAnalysisProgressUnavailable):
		api.WriteAppError(w, api.Internal(action, "Analysis progress could not be saved. Read the retained overview, then resume or cancel explicitly.", err))
	case errors.Is(err, app.ErrNewerAnalysisRun):
		api.WriteAppError(w, api.Conflict(action, "A newer Kogin saved this project's analysis progress. Upgrade Kogin to continue it, or move .kogin/analysis/run.json away.", err))
	case errors.Is(err, app.ErrIssueSelection):
		api.WriteAppError(w, api.BadRequest(action, "Choose up to five open issues of one file to fix together.", err))
	case errors.Is(err, app.ErrUnknownDocsComponent):
		api.WriteAppError(w, api.NotFound(action, "The docs show no component at this path any more. Open Docs again.", err))
	case errors.Is(err, app.ErrDocsRefreshBusy):
		api.WriteAppError(w, api.Conflict(action, "The docs are already being refreshed. Wait for that refresh to finish.", err))
	case errors.Is(err, app.ErrAnalysisRunBusy):
		api.WriteAppError(w, api.Conflict(action, "An analysis run is already active or paused. Continue or cancel it before starting a new analysis.", err))
	case errors.Is(err, project.ErrNoActiveProject):
		api.WriteAppError(w, api.NotFound(action, "Import a project before using this endpoint.", err))
	case errors.Is(err, project.ErrExcludedFile):
		api.WriteAppError(w, api.Forbidden(action, "This file is excluded by the project context policy.", err))
	case errors.Is(err, app.ErrAgentSettingsStale):
		api.WriteAppError(w, api.Conflict(action, agentSettingsStaleMessage, err))
	case errors.Is(err, project.ErrRevisionConflict):
		api.WriteAppError(w, api.Conflict(action, "The active project changed. Refresh and try again.", err))
	case errors.Is(err, project.ErrUnsupportedFile):
		api.WriteAppError(w, api.NewAppError(api.ErrorBadRequest, action, "This file does not support explanation.", http.StatusUnprocessableEntity, err))
	case errors.Is(err, project.ErrSecurityRulesUnavailable):
		api.WriteAppError(w, api.NewAppError(api.ErrorBadRequest, action, "Security source-only scanning is unavailable for this file language.", http.StatusUnprocessableEntity, err))
	default:
		api.WriteAppError(w, api.BadRequest(action, "Review the request and try again.", err))
	}
}
