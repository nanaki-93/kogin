package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
)

func TestAgentSettingsHTTPRequiresCurrentConfirmedCompleteValues(t *testing.T) {
	fixture := newChangeHandlerFixture(t, "http://127.0.0.1:1")
	cfg := scopedHandlerConfig("http://127.0.0.1:1")
	cfg.ModelScopes.Code.APIKey = "SETTINGS_SECRET_MUST_NOT_LEAK"
	service, err := app.New(cfg, fixture.manager)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewAgentSettingsHandler(service)
	index, err := fixture.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"project_id": {index.ProjectID}, "project_revision": {index.ProjectRevision}}
	w := httptest.NewRecorder()
	handler.Get(w, httptest.NewRequest("GET", "/?"+query.Encode(), nil))
	var view app.AgentSettings
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || view.State != "configured" || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), cfg.ModelScopes.Code.APIKey) {
		t.Fatal("settings inventory", w.Code, w.Body.String(), err)
	}
	post := func(data []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.Control(w, httptest.NewRequest("POST", "/", bytes.NewReader(data)))
		return w
	}
	request := app.AgentSettingsControl{ProjectID: view.ProjectID, ProjectRevision: view.ProjectRevision, Configuration: view.Configuration, Version: view.Version, Action: "replace", Values: &agentstate.AgentPreferences{Roles: view.Roles, Limits: view.Limits}}
	data, _ := json.Marshal(request)
	if w := post(data); w.Code != http.StatusBadRequest {
		t.Fatal("missing confirmation accepted", w.Code, w.Body.String())
	}
	request.Confirm = true
	data, _ = json.Marshal(request)
	for _, bad := range [][]byte{
		append(append([]byte{}, data...), []byte(` {}`)...),
		[]byte(strings.TrimSuffix(string(data), "}") + `,"apply":true}`),
		[]byte(strings.TrimSuffix(string(data), "}") + `,"confirm":false}`),
		[]byte(strings.Replace(string(data), `"limits":{`, `"limits":{"unlimited":true,`, 1)),
		[]byte(strings.Replace(string(data), `"repairs":2`, `"repairs":null`, 1)),
		[]byte(strings.Replace(string(data), `,"repairs":2`, ``, 1)),
	} {
		if w := post(bad); w.Code != http.StatusBadRequest {
			t.Fatal("ambiguous settings accepted", w.Code, w.Body.String())
		}
	}
	request.Values.Limits.ModelCalls = 10
	data, _ = json.Marshal(request)
	w = post(data)
	var saved app.AgentSettings
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || w.Code != 200 || saved.Version != 1 || saved.Limits.ModelCalls != 10 || saved.State != "overridden" {
		t.Fatal("replace", w.Code, w.Body.String(), err)
	}
	if w := post(data); w.Code != http.StatusConflict {
		t.Fatal("stale version accepted", w.Code, w.Body.String())
	}
	request.Action, request.Version, request.Values = "reset", saved.Version, nil
	data, _ = json.Marshal(request)
	w = post(data)
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || w.Code != 200 || saved.State != "configured" || saved.Version != 2 {
		t.Fatal("reset", w.Code, w.Body.String(), err)
	}
	for _, suffix := range []string{"", "?" + query.Encode() + "&connect=true", "?" + query.Encode() + "&project_id=another"} {
		w := httptest.NewRecorder()
		handler.Get(w, httptest.NewRequest("GET", "/"+suffix, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatal("invalid settings query accepted", suffix, w.Code)
		}
	}
}
