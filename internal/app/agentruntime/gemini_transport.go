package agentruntime

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

const nativeProviderByteLimit = 4 * 1024 * 1024

// Each SDK generation gets one exchange. This also rejects SDK continuation
// requests, which otherwise evade the application's attempt accounting.
type geminiTransport struct {
	profile    config.ModelProfile
	base       *url.URL
	transport  http.RoundTripper
	dispatched atomic.Bool
}

func (g *geminiTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := g.admit(request); err != nil {
		return nil, err
	}
	bound := request.Clone(request.Context())
	bound.Header.Del("Authorization")
	bound.Header.Del("X-Goog-Api-Key")
	if g.profile.AccessToken != "" {
		bound.Header.Set("Authorization", "Bearer "+g.profile.AccessToken)
	} else {
		bound.Header.Set("X-Goog-Api-Key", g.profile.APIKey)
	}
	response, err := g.transport.RoundTrip(bound)
	if err != nil {
		return nil, err
	}
	if err := nativeHTTPStatus(response.StatusCode); err != nil {
		response.Body.Close()
		return nil, err
	}
	response.Body = &nativeBoundedBody{ReadCloser: response.Body, remaining: nativeProviderByteLimit}
	return response, nil
}

func (g *geminiTransport) admit(request *http.Request) error {
	if err := request.Context().Err(); err != nil {
		return err
	}
	target := request.URL
	if target.Scheme != g.base.Scheme || target.Host != g.base.Host || target.User != nil || request.Host != "" && request.Host != g.base.Host {
		return fmt.Errorf("%w: native provider changed its configured endpoint", llm.ErrRequestRejected)
	}
	if request.Method != http.MethodPost || !strings.HasPrefix(target.EscapedPath(), strings.TrimRight(g.base.EscapedPath(), "/")+"/") {
		return fmt.Errorf("%w: unsupported native provider operation", llm.ErrRequestRejected)
	}
	if request.ContentLength < 0 || request.ContentLength > nativeProviderByteLimit {
		return fmt.Errorf("%w: native request exceeds byte limit", llm.ErrRequestRejected)
	}
	if !g.dispatched.CompareAndSwap(false, true) {
		return fmt.Errorf("%w: SDK continuation or retry requires a new admitted attempt", llm.ErrRequestRejected)
	}
	return nil
}

func nativeHTTPStatus(status int) error {
	switch {
	case status >= 300 && status < 400:
		return llm.ErrRedirectRejected
	case status == 400 || status == 401 || status == 403 || status == 404 || status == 422:
		return &nativeStatusError{status: status, cause: llm.ErrRequestRejected}
	case status < 200 || status >= 300:
		return &nativeStatusError{status: status}
	default:
		return nil
	}
}

type nativeStatusError struct {
	status int
	cause  error
}

func (e *nativeStatusError) Error() string {
	return fmt.Sprintf("native provider HTTP status %d", e.status)
}
func (e *nativeStatusError) Unwrap() error { return e.cause }

type nativeBoundedBody struct {
	io.ReadCloser
	remaining int
}

func (b *nativeBoundedBody) Read(buffer []byte) (int, error) {
	if len(buffer) > b.remaining+1 {
		buffer = buffer[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(buffer)
	if n > b.remaining {
		return 0, fmt.Errorf("%w: native response exceeds byte limit", llm.ErrUnusableResponse)
	}
	b.remaining -= n
	return n, err
}
