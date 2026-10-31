package handlers

import (
	"errors"
	"net/http"

	"github.com/nanaki-93/mini-orca/v2/internal/api"
	"github.com/nanaki-93/mini-orca/v2/internal/app"
)

type securityScanRequest struct {
	Path            string `json:"path"`
	ProjectRevision string `json:"project_revision"`
}

type declarationExplanationRequest struct {
	ProjectID             string `json:"project_id"`
	ProjectRevision       string `json:"project_revision"`
	BaseFileHash          string `json:"base_file_hash"`
	TargetPath            string `json:"target_path"`
	ConfirmRemoteProvider *bool  `json:"confirm_remote_provider"`
}

type securityReviewRequest struct {
	ProjectID             string `json:"project_id"`
	ProjectRevision       string `json:"project_revision"`
	BaseFileHash          string `json:"base_file_hash"`
	Path                  string `json:"path"`
	ConfirmRemoteProvider *bool  `json:"confirm_remote_provider"`
}

// FileAnalysis returns cached semantic state only; source is never returned.
func (h *ProjectHandler) FileAnalysis(w http.ResponseWriter, r *http.Request) {
	if !requireCurrentRevision(w, h.manager, r.URL.Query().Get("project_revision")) {
		return
	}
	analysis, err := h.service.CachedFileAnalysis(r.URL.Query().Get("path"))
	if err != nil {
		writeProjectError(w, "file analysis lookup failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, analysis)
}

// ScanSecurityFile runs the bounded deterministic rules against one selected
// Go file. It is source-only and does not contact a provider or run project code.
func (h *ProjectHandler) ScanSecurityFile(w http.ResponseWriter, r *http.Request) {
	var request securityScanRequest
	if !decodeStrictJSON(w, r, &request, "invalid security scan request", "Provide path and project_revision.") || !requireCurrentRevision(w, h.manager, request.ProjectRevision) {
		return
	}
	report, err := h.service.ScanSecurityFile(r.Context(), request.Path, request.ProjectRevision)
	if err != nil {
		if writeContextError(w, "security scan", err) {
			return
		}
		writeProjectError(w, "security scan failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, report)
}

// ExplainDeclaration performs one transient, read-only Code-scope request.
func (h *ProjectHandler) ExplainDeclaration(w http.ResponseWriter, r *http.Request) {
	var request declarationExplanationRequest
	if err := api.DecodeJSON(w, r, &request); err != nil {
		api.WriteRequestError(w, err, "invalid declaration explanation request", "Provide the current project and file identity.")
		return
	}
	if request.ConfirmRemoteProvider == nil {
		api.WriteRequestError(w, errors.New("confirm_remote_provider is required"), "invalid declaration explanation request", "Confirm whether the Code provider may receive this file context.")
		return
	}
	result, err := h.service.ExplainDeclaration(r.Context(), app.DeclarationExplanationRequest{
		ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision,
		BaseFileHash: request.BaseFileHash, TargetPath: request.TargetPath,
		ConfirmRemoteProvider: *request.ConfirmRemoteProvider,
	})
	if err != nil {
		if writeContextError(w, "declaration explanation", err) {
			return
		}
		writeProjectError(w, "declaration explanation failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, result)
}

// ReviewSecurityFile performs one explicit, passive Analyze-scope review of a
// selected eligible file. It never starts a scan or executes project code.
func (h *ProjectHandler) ReviewSecurityFile(w http.ResponseWriter, r *http.Request) {
	var request securityReviewRequest
	if !decodeStrictJSON(w, r, &request, "invalid security review request", "Provide the current project, file identity, and remote-provider confirmation.") {
		return
	}
	if request.ConfirmRemoteProvider == nil {
		api.WriteRequestError(w, errors.New("confirm_remote_provider is required"), "invalid security review request", "Confirm whether the Analyze provider may receive this file context.")
		return
	}
	report, err := h.service.ReviewSecurityFile(r.Context(), app.SecurityReviewRequest{
		ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision, BaseFileHash: request.BaseFileHash,
		Path: request.Path, ConfirmRemoteProvider: *request.ConfirmRemoteProvider,
	})
	if err != nil {
		if writeContextError(w, "security review", err) {
			return
		}
		writeProjectError(w, "security review failed", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, report)
}
