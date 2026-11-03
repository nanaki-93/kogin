package handlers

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// WebUICSP restricts the embedded local Web UI to same-origin scripts, styles,
// fonts and API requests. No inline script or style is permitted.
const WebUICSP = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// webUIStyleNoncePlaceholder is replaced in the document with a fresh nonce per
// response. Dialog scroll locking injects one <style> tag carrying that nonce;
// no other inline style or any inline script is admitted.
const webUIStyleNoncePlaceholder = "__KOGIN_STYLE_NONCE__"

// webUIWriteTokenPlaceholder is replaced with the daemon's write token, which
// the Web UI sends with every request that changes state.
const webUIWriteTokenPlaceholder = "__KOGIN_WRITE_TOKEN__"

// WebUIDocumentCSP is the policy for the document itself: WebUICSP plus the
// per-response style nonce.
func WebUIDocumentCSP(nonce string) string {
	return strings.Replace(WebUICSP, "style-src 'self'", "style-src 'self' 'nonce-"+nonce+"'", 1)
}

func newStyleNonce() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(raw), nil
}

// The Web UI is a React + TypeScript application in web/. `make webui-build`
// compiles it into webui/dist, which is committed and embedded so the daemon
// needs no Node.js at runtime.
//
//go:embed webui/dist
var webUIFiles embed.FS

var webUIContentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".woff2": "font/woff2",
	".svg":   "image/svg+xml",
}

// WebUIHandler serves the embedded local Web UI and its static assets.
type WebUIHandler struct {
	files fs.FS
	token string
}

// NewWebUIHandler constructs the handler for GET /ui and GET /ui/assets/{path...};
// the document carries the daemon's write token.
func NewWebUIHandler(token string) *WebUIHandler {
	files, err := fs.Sub(webUIFiles, "webui/dist")
	if err != nil {
		panic("embedded web UI is missing: " + err.Error())
	}
	return newWebUIHandler(files, token)
}

func newWebUIHandler(files fs.FS, token string) *WebUIHandler {
	return &WebUIHandler{files: files, token: token}
}

// Serve writes the Web UI document with strict local security headers. The
// document is never cached so a rebuilt daemon always loads its own bundle,
// and every response carries its own style nonce.
func (h *WebUIHandler) Serve(w http.ResponseWriter, _ *http.Request) {
	data, err := fs.ReadFile(h.files, "index.html")
	if err != nil {
		http.Error(w, "web UI unavailable", http.StatusInternalServerError)
		return
	}
	nonce, err := newStyleNonce()
	if err != nil {
		http.Error(w, "web UI unavailable", http.StatusInternalServerError)
		return
	}
	writeWebUIHeaders(w, "text/html; charset=utf-8", "no-store")
	w.Header().Set("Content-Security-Policy", WebUIDocumentCSP(nonce))
	w.WriteHeader(http.StatusOK)
	data = bytes.ReplaceAll(data, []byte(webUIStyleNoncePlaceholder), []byte(nonce))
	_, _ = w.Write(bytes.ReplaceAll(data, []byte(webUIWriteTokenPlaceholder), []byte(h.token)))
}

// ServeAsset writes one embedded bundle file. Only clean, top-level paths with
// a known asset extension are served; the document, directories and other
// files are not found. Bundle file names carry a content hash, so they are
// cached as immutable.
func (h *WebUIHandler) ServeAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("path")
	contentType, ok := webUIContentTypes[path.Ext(name)]
	if !ok || !validAssetName(name) {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(h.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeWebUIHeaders(w, contentType, "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func validAssetName(name string) bool {
	return name != "" && path.Clean(name) == name && !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..") && fs.ValidPath(name)
}

func writeWebUIHeaders(w http.ResponseWriter, contentType, cacheControl string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Content-Security-Policy", WebUICSP)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
