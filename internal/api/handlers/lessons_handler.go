package handlers

import (
	"net/http"

	"github.com/nanaki-93/kogin/v2/internal/api"
	"github.com/nanaki-93/kogin/v2/internal/app"
)

// Lessons returns the project's learning log: what accepted tasks taught and
// the Ask the code answers saved to it, newest first, with concept counts.
func (h *ProjectHandler) Lessons(w http.ResponseWriter, r *http.Request) {
	if !requireCurrentRevision(w, h.manager, r.URL.Query().Get("project_revision")) {
		return
	}
	lessons, err := h.service.Lessons(r.Context())
	if err != nil {
		writeProjectError(w, "learning log unavailable", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, lessons)
}

// SaveLesson saves an Ask the code answer to the learning log.
func (h *ProjectHandler) SaveLesson(w http.ResponseWriter, r *http.Request) {
	var request app.AskLessonRequest
	if !decodeStrictJSON(w, r, &request, "invalid lesson request", "Provide the question, its answer and the project identity.") {
		return
	}
	lessons, err := h.service.AddAskLesson(r.Context(), request)
	if err != nil {
		api.WriteAppError(w, api.BadRequest("lesson not saved", err.Error(), err))
		return
	}
	api.WriteJSON(w, http.StatusOK, lessons)
}
