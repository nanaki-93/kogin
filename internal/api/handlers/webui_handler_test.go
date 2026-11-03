package handlers

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func webUIMux(handler *WebUIHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui", handler.Serve)
	mux.HandleFunc("GET /ui/assets/{path...}", handler.ServeAsset)
	return mux
}

func assertWebUISecurityHeaders(t *testing.T, res *httptest.ResponseRecorder, contentType, cacheControl string) {
	t.Helper()
	for header, want := range map[string]string{
		"Content-Type":            contentType,
		"Cache-Control":           cacheControl,
		"Content-Security-Policy": WebUICSP,
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := res.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

var bundleReference = regexp.MustCompile(`(?:src|href)="/ui/assets/([^"]+)"`)

func TestWebUIHandlerServesDocumentWithStrictSecurityHeaders(t *testing.T) {
	res := httptest.NewRecorder()
	webUIMux(NewWebUIHandler("test-write-token")).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	body := res.Body.String()
	nonce := regexp.MustCompile(`<meta name="kogin-style-nonce" content="([A-Za-z0-9+/]{24})"`).FindStringSubmatch(body)
	if nonce == nil {
		t.Fatalf("web UI document carries no per-response style nonce:\n%s", body)
	}
	if strings.Contains(body, webUIStyleNoncePlaceholder) {
		t.Fatal("style nonce placeholder was not replaced")
	}
	if !strings.Contains(body, `<meta name="kogin-write-token" content="test-write-token"`) || strings.Contains(body, webUIWriteTokenPlaceholder) {
		t.Fatalf("web UI document does not carry the write token:\n%s", body)
	}
	if got, want := res.Header().Get("Content-Security-Policy"), WebUIDocumentCSP(nonce[1]); got != want || !strings.Contains(got, "style-src 'self' 'nonce-"+nonce[1]+"'") {
		t.Fatalf("document CSP = %q, want %q", got, want)
	}
	res.Header().Set("Content-Security-Policy", WebUICSP)
	assertWebUISecurityHeaders(t, res, "text/html; charset=utf-8", "no-store")
	second := httptest.NewRecorder()
	webUIMux(NewWebUIHandler("test-write-token")).ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if strings.Contains(second.Body.String(), nonce[1]) {
		t.Fatal("style nonce must differ per response")
	}
	if !strings.Contains(body, `<div id="root"></div>`) {
		t.Fatal("web UI document has no React root")
	}
	if !regexp.MustCompile(`<script type="module"[^>]* src="/ui/assets/app-[^"]+\.js"`).MatchString(body) {
		t.Fatalf("web UI document does not load the bundled app module:\n%s", body)
	}
	if strings.Contains(body, " style=") || regexp.MustCompile(`<script>`).MatchString(body) || strings.Contains(body, "<style") {
		t.Fatal("web UI document must not contain inline scripts or styles; the CSP forbids them")
	}
	for _, unsafe := range []string{"unsafe-inline", "unsafe-eval", "http:", "https:"} {
		if strings.Contains(WebUICSP, unsafe) {
			t.Fatalf("CSP must not allow %s: %s", unsafe, WebUICSP)
		}
	}
}

// Every file the document references must be embedded and served, otherwise
// the browser fails to boot the app with no server-side signal.
func TestWebUIBundleReferencesResolveToServedAssets(t *testing.T) {
	handler := NewWebUIHandler("test-write-token")
	mux := webUIMux(handler)
	index, err := fs.ReadFile(handler.files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	refs := bundleReference.FindAllStringSubmatch(string(index), -1)
	if len(refs) < 2 {
		t.Fatalf("expected the document to reference a script and a stylesheet, found %d references", len(refs))
	}
	for _, ref := range refs {
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/ui/assets/"+ref[1], nil))
		if res.Code != http.StatusOK || res.Body.Len() == 0 {
			t.Fatalf("GET /ui/assets/%s = %d (%d bytes), want a served asset", ref[1], res.Code, res.Body.Len())
		}
		assertWebUISecurityHeaders(t, res, webUIContentTypes[ref[1][strings.LastIndex(ref[1], "."):]], "public, max-age=31536000, immutable")
	}
}

// The bundled fonts are referenced from the stylesheet; they must be embedded
// and served with a font content type the CSP font-src allows.
func TestWebUIStylesheetFontsAreServed(t *testing.T) {
	handler := NewWebUIHandler("test-write-token")
	mux := webUIMux(handler)
	entries, err := fs.ReadDir(handler.files, ".")
	if err != nil {
		t.Fatal(err)
	}
	fonts := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			t.Fatalf("bundle must be flat; found directory %s", name)
		}
		if !strings.HasSuffix(name, ".css") {
			continue
		}
		css, err := fs.ReadFile(handler.files, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range regexp.MustCompile(`url\(/ui/assets/([^)]+\.woff2)\)`).FindAllStringSubmatch(string(css), -1) {
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/ui/assets/"+match[1], nil))
			if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "font/woff2" {
				t.Fatalf("font %s = %d %q", match[1], res.Code, res.Header().Get("Content-Type"))
			}
			fonts++
		}
	}
	if fonts == 0 {
		t.Fatal("stylesheet references no bundled fonts")
	}
}

func TestWebUIAssetsRejectUnsafeOrUnknownPaths(t *testing.T) {
	handler := newWebUIHandler(fstest.MapFS{
		"index.html":       {Data: []byte("<div id=\"root\"></div>")},
		"app-1.js":         {Data: []byte("export {}")},
		"style-1.css":      {Data: []byte("body{}")},
		"nested/app-2.js":  {Data: []byte("export {}")},
		"notes.txt":        {Data: []byte("secret")},
		"webui_handler.go": {Data: []byte("package handlers")},
	}, "test-write-token")
	mux := webUIMux(handler)
	for _, path := range []string{"/ui/assets/app-1.js", "/ui/assets/style-1.css"} {
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, res.Code)
		}
	}
	for _, path := range []string{
		"/ui/assets/",
		"/ui/assets/nested",
		"/ui/assets/nested/app-2.js",
		"/ui/assets/index.html",
		"/ui/assets/notes.txt",
		"/ui/assets/missing.js",
		"/ui/assets/nested/../app-1.js",
		"/ui/assets/./app-1.js",
		"/ui/assets/%2e%2e/webui_handler.go",
		"/ui/assets/webui_handler.go",
		`/ui/assets/..%5capp-1.js`,
	} {
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code == http.StatusOK {
			t.Fatalf("GET %s = 200, want rejection", path)
		}
	}
}

func TestWebUIHandlerReportsMissingDocument(t *testing.T) {
	res := httptest.NewRecorder()
	webUIMux(newWebUIHandler(fstest.MapFS{}, "test-write-token")).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
}
