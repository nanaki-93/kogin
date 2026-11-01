package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/nanaki-93/kasuri/v2/internal/brand"
)

// WriteTokenHeader carries the daemon's per-launch write token.
const WriteTokenHeader = "X-Kasuri-Token"

// RequireWriteToken admits a request that can change state (any method but
// GET, HEAD and OPTIONS) only when it carries this launch's write token. The
// Web UI receives the token in its document; other local clients read it from
// tokenFile, which only the daemon's user can read. Reads stay open so status
// probes and event streams need no token.
func RequireWriteToken(next http.Handler, token, tokenFile string) http.Handler {
	hint := "Reload the Web UI to get this daemon's write token."
	if tokenFile != "" {
		hint += " Other local clients send the token in " + WriteTokenHeader + "; it is in " + tokenFile + "."
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		sent := r.Header.Get(WriteTokenHeader)
		if token == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(token)) != 1 {
			WriteAppError(w, NewAppError(ErrorUnauthorized, "missing or stale write token", hint, http.StatusUnauthorized, nil))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WriteTokenFile is where the daemon saves its write token for local clients:
// KASURI_TOKEN_FILE when set (an absolute path), otherwise
// kasuri/daemon-token in the user configuration directory.
func WriteTokenFile() (string, error) {
	if path := brand.Getenv("KASURI_TOKEN_FILE"); path != "" {
		if !filepath.IsAbs(path) {
			return "", fmt.Errorf("KASURI_TOKEN_FILE must be an absolute path")
		}
		return path, nil
	}
	directory, err := brand.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the write token directory: %w", err)
	}
	return filepath.Join(directory, "daemon-token"), nil
}
