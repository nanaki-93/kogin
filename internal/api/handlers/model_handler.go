package handlers

import (
	"net/http"

	"github.com/nanaki-93/kasuri/v2/internal/api"
	"github.com/nanaki-93/kasuri/v2/internal/app"
)

type ModelHandler struct{ service *app.Service }

func NewModelHandler(service *app.Service) *ModelHandler { return &ModelHandler{service: service} }

func (h *ModelHandler) Current(w http.ResponseWriter, _ *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.service.CurrentModelCatalog())
}

func (h *ModelHandler) Available(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		api.WriteAppError(w, api.BadRequest("invalid model catalog query", "The model catalog accepts no query parameters.", nil))
		return
	}
	catalog, err := h.service.AvailableModels(r.Context())
	if err != nil {
		writeProjectError(w, "model catalog unavailable", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, catalog)
}
