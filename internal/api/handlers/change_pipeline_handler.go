package handlers

import (
	"net/http"

	"github.com/nanaki-93/kasuri/v2/internal/api"
	"github.com/nanaki-93/kasuri/v2/internal/app"
)

func (h *ChangeHandler) StartWorkflow(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeWorkflowRequest
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.StartChangeWorkflow(r.Context(), r.PathValue("sessionID"), request)
	if err != nil {
		respondWorkflow(w, nil, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, value)
}

func (h *ChangeHandler) CancelWorkflow(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeWorkflowControl
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.CancelChangeWorkflow(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) RespondInput(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeInputResponse
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.RespondChangeInput(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}

func (h *ChangeHandler) OverrideReview(w http.ResponseWriter, r *http.Request) {
	var request app.ChangeReviewOverride
	if !decodeWorkflowBody(w, r, &request) {
		return
	}
	value, err := h.service.OverrideChangeReview(r.Context(), r.PathValue("sessionID"), request)
	respondWorkflow(w, value, err)
}
