package handlers

import (
	"net/http"

	"github.com/nanaki-93/kasuri/v2/internal/api"
)

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
