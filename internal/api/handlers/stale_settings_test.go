package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestStaleAgentSettingsSayWhatToReset(t *testing.T) {
	err := fmt.Errorf("%w: %w", project.ErrRevisionConflict, app.ErrAgentSettingsStale)
	for name, write := range map[string]func(http.ResponseWriter, string, error){
		"project":  writeProjectError,
		"workflow": writeWorkflowError,
	} {
		response := httptest.NewRecorder()
		write(response, "analysis preview failed", err)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "Reset them there, or save setup") || strings.Contains(response.Body.String(), "The active project changed") {
			t.Errorf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
}
