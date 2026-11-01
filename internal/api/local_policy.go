package api

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// HostPolicy decides which Host headers may reach the API.
type HostPolicy struct {
	// External is set when the daemon was deliberately bound beyond loopback.
	External bool
	// Names are extra host names accepted when External. IP literals are always
	// accepted then: DNS rebinding needs a name, so an address cannot be
	// rebound to the daemon by a web page.
	Names []string
}

// allows reports whether a request for host may reach the API.
func (p HostPolicy) allows(host string) bool {
	if isLoopbackHost(host) {
		return true
	}
	if !p.External {
		return false
	}
	hostname := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		hostname = parsed
	}
	hostname = strings.Trim(hostname, "[]")
	if net.ParseIP(hostname) != nil {
		return true
	}
	for _, name := range p.Names {
		if strings.EqualFold(strings.TrimSpace(name), hostname) {
			return true
		}
	}
	return false
}

// LocalOnly protects the daemon's native-client API boundary before route
// handlers can observe a request. Other hosts are accepted only when the daemon
// was deliberately bound beyond loopback, and host names only when listed, so
// a web page cannot reach the API by rebinding its own name to the daemon.
// Accepted hosts are not authenticated.
func LocalOnly(next http.Handler, hosts HostPolicy) http.Handler {
	allowExternalHosts := hosts.External
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts.allows(r.Host) {
			hint := "Use the local Kasuri daemon address."
			if allowExternalHosts {
				hint = "Use an IP address or a host name listed in KASURI_ALLOWED_HOSTS."
			}
			WriteAppError(w, Forbidden("unexpected Host header", hint, nil))
			return
		}
		if isCORSPreflight(r) {
			WriteAppError(w, Forbidden("browser preflight requests are not supported", "Use the local Kasuri client.", nil))
			return
		}
		if r.Header.Get("Origin") != "" && !isAllowedWebUIOrigin(r, allowExternalHosts) && !isSameOriginWebUIAsset(r, allowExternalHosts) {
			WriteAppError(w, Forbidden("browser Origin headers are not supported", "Use the local Kasuri client.", nil))
			return
		}
		if requiresJSON(r.Method) && requestHasBody(r) && !isJSONContentType(r.Header.Get("Content-Type")) {
			WriteAppError(w, NewAppError(ErrorBadRequest, "application/json Content-Type is required", "Send this request as JSON.", http.StatusUnsupportedMediaType, nil))
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isAllowedWebUIOrigin(r *http.Request, allowExternalHosts bool) bool {
	if r.Header.Get("X-Kasuri-Web-UI") != "1" {
		return false
	}
	return isSameDaemonOrigin(r, allowExternalHosts)
}

// isSameOriginWebUIAsset admits the Origin header browsers attach to ES module
// script fetches. Only side-effect-free GET/HEAD requests for the embedded Web UI
// shell and its static assets qualify, and only from the daemon's own origin.
func isSameOriginWebUIAsset(r *http.Request, allowExternalHosts bool) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.URL.Path != "/ui" && !strings.HasPrefix(r.URL.Path, "/ui/assets/") {
		return false
	}
	return isSameDaemonOrigin(r, allowExternalHosts)
}

func isSameDaemonOrigin(r *http.Request, allowExternalHosts bool) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin == nil || origin.Scheme != "http" || origin.Host == "" {
		return false
	}
	if !strings.EqualFold(origin.Host, r.Host) {
		return false
	}
	return allowExternalHosts || isLoopbackHost(origin.Host)
}

func isLoopbackHost(host string) bool {
	hostname := host
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		hostname = parsedHost
	}
	hostname = strings.Trim(hostname, "[]")
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func isCORSPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func requiresJSON(method string) bool {
	return method == http.MethodPost || method == http.MethodPatch
}

func requestHasBody(r *http.Request) bool {
	return r.Body != nil && r.ContentLength != 0
}

func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}
