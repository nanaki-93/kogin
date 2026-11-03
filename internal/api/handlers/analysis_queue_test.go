package handlers

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
)

func TestAnalysisSelectionHandlerGuardsAndPersistence(t *testing.T) {
	h, _, analysis, calls := newAnalysisHandlerFixture(t)
	query := url.Values{"project_id": {analysis.ProjectID}, "project_revision": {analysis.ProjectRevision}}
	target := "/analysis/selection?" + query.Encode()
	w := analysisHandlerRequest(t, h.Selection, "GET", target, nil)
	if w.Code != 200 {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	var selection app.AnalysisFileSelection
	if err := json.Unmarshal(w.Body.Bytes(), &selection); err != nil {
		t.Fatal(err)
	}
	if len(selection.Files) != 1 || len(selection.Files[0].Stages) != 4 || selection.Files[0].Stages[0].Status != "missing" || selection.Files[0].Stages[0].Reason == "" {
		t.Fatalf("missing per-file analysis status: %+v", selection.Files)
	}
	request := app.AnalysisSelectionRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, SelectionID: selection.SelectionID, ExcludedPaths: []string{"main.go"}}
	w = analysisHandlerRequest(t, h.SaveSelection, "POST", "/analysis/selection", request)
	if w.Code != 200 || calls.Load() != 0 {
		t.Fatalf("save: %d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	w = analysisHandlerRequest(t, h.Selection, "GET", target, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &selection); err != nil {
		t.Fatal(err)
	}
	if len(selection.ExcludedPaths) != 1 || selection.ExcludedPaths[0] != "main.go" {
		t.Fatalf("selection: %+v", selection)
	}
	w = analysisHandlerRequest(t, h.SaveSelection, "POST", "/analysis/selection", request)
	if w.Code != 409 {
		t.Fatalf("stale save: %d", w.Code)
	}
	for _, body := range []string{`{}`, `{"project_id":"p","project_revision":"r","selection_id":"s","excluded_paths":["../x"]}`, `{"unknown":true}`} {
		w = analysisHandlerRequest(t, h.SaveSelection, "POST", "/analysis/selection", body)
		if w.Code != 400 {
			t.Fatalf("invalid save: %d %s", w.Code, w.Body)
		}
		assertStructuredError(t, w)
	}
	w = analysisHandlerRequest(t, h.Selection, "GET", target+"&project_id=other", nil)
	if w.Code != 400 {
		t.Fatalf("duplicate guard: %d", w.Code)
	}
	query.Set("project_revision", "stale")
	w = analysisHandlerRequest(t, h.Selection, "GET", "/analysis/selection?"+query.Encode(), nil)
	if w.Code != 409 {
		t.Fatalf("stale read: %d", w.Code)
	}
}
