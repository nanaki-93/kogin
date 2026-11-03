package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteTokenGuardsEveryChangingRequest(t *testing.T) {
	reached := 0
	handler := RequireWriteToken(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(http.StatusNoContent) }), "launch-token", "/home/me/.config/kogin/daemon-token")
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/api/projects/current", nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s needed a token: %d", method, response.Code)
		}
	}
	for _, sent := range []string{"", "other-token", "launch-token-and-more"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			request := httptest.NewRequest(method, "/api/projects/current/changes", strings.NewReader("{}"))
			if sent != "" {
				request.Header.Set(WriteTokenHeader, sent)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "daemon-token") {
				t.Fatalf("%s with %q: %d %s", method, sent, response.Code, response.Body)
			}
		}
	}
	before := reached
	request := httptest.NewRequest(http.MethodPost, "/api/projects/current/changes", strings.NewReader("{}"))
	request.Header.Set(WriteTokenHeader, "launch-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || reached != before+1 {
		t.Fatalf("token request refused: %d", response.Code)
	}
	empty := RequireWriteToken(http.NotFoundHandler(), "", "")
	refused := httptest.NewRecorder()
	empty.ServeHTTP(refused, httptest.NewRequest(http.MethodPost, "/", nil))
	if refused.Code != http.StatusUnauthorized {
		t.Fatal("an empty token admitted writes")
	}
}
