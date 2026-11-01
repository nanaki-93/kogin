package handlers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/api"
	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func writeWorkflowError(w http.ResponseWriter, operation string, err error) {
	if errors.Is(err, app.ErrAgentSettingsStale) {
		api.WriteAppError(w, api.Conflict(operation, agentSettingsStaleMessage, err))
		return
	}
	if errors.Is(err, project.ErrRevisionConflict) {
		api.WriteError(w, http.StatusConflict, "project or file changed; refresh the proposal")
		return
	}
	if errors.Is(err, context.Canceled) {
		api.WriteError(w, http.StatusRequestTimeout, "generation canceled")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		api.WriteError(w, http.StatusGatewayTimeout, "generation timed out")
		return
	}
	if errors.Is(err, app.ErrChangeInUse) {
		api.WriteAppError(w, api.Conflict(operation, "This task is in use: "+strings.TrimPrefix(err.Error(), app.ErrChangeInUse.Error()+": ")+".", err))
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		api.WriteAppError(w, api.NotFound(operation, "This task no longer exists; refresh the task list.", err))
		return
	}
	api.WriteAppError(w, api.BadRequest(operation, "Review the request and try again.", err))
}
