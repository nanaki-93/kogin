package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestAWorkflowConflictSaysWhatChanged(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: the selected model agy:gone is not available", project.ErrRevisionConflict), "the selected model agy:gone is not available"},
		{project.ErrRevisionConflict, "project or file changed; refresh the proposal"},
		{fmt.Errorf("load: %w", project.ErrRevisionConflict), "project or file changed; refresh the proposal"},
	} {
		recorder := httptest.NewRecorder()
		writeWorkflowError(recorder, "start workflow", tc.err)
		var body struct {
			Message string `json:"message"`
		}
		if recorder.Code != http.StatusConflict || json.Unmarshal(recorder.Body.Bytes(), &body) != nil || body.Message != tc.want {
			t.Fatalf("%v: %d %s", tc.err, recorder.Code, recorder.Body.String())
		}
	}
}
