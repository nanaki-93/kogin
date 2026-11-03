package app

import (
	"errors"
	"net"
	"syscall"

	"github.com/nanaki-93/kogin/v2/internal/llm"
)

// Saved stage reasons are a closed set, so a failure is described by its kind,
// never by provider text that could carry source or credentials.
const (
	reasonModelFailed      = "The model request or response failed. Other analysis results remain available."
	reasonModelUnreachable = "The model provider could not be reached. Check that it is running and that its address in Settings → Models & agents is right."
	reasonModelTimedOut    = "The model did not answer in time. Retry, or raise the analysis time limit in Settings → Models & agents."
	reasonModelRejected    = "The model provider rejected the request. Check the model name, credentials or CLI in Settings → Models & agents."
	reasonModelUnusable    = "The model answered, but not with a usable report. Retry, or choose another model."
)

// modelFailureReasons are every reason modelFailureReason can return, and the
// earlier wording of some that saved runs may still hold.
var modelFailureReasons = []string{reasonModelFailed, reasonModelUnreachable, reasonModelTimedOut, reasonModelRejected, reasonModelUnusable,
	legacyReasonModelUnreachable, legacyReasonModelTimedOut, legacyReasonModelRejected}

// Earlier wording that pointed to config.yaml before setup and Settings could
// change models and time limits.
const (
	legacyReasonModelUnreachable = "The model provider could not be reached. Check that it is running and that config.yaml names the right address."
	legacyReasonModelTimedOut    = "The model did not answer in time. Retry, or raise timeouts in config.yaml."
	legacyReasonModelRejected    = "The model provider rejected the request. Check the model name, credentials or CLI in config.yaml."
)

// currentModelFailureReason gives a saved reason its current wording.
func currentModelFailureReason(reason string) string {
	switch reason {
	case legacyReasonModelUnreachable:
		return reasonModelUnreachable
	case legacyReasonModelTimedOut:
		return reasonModelTimedOut
	case legacyReasonModelRejected:
		return reasonModelRejected
	}
	return reason
}

// modelFailureReason names what kind of failure a model request had.
func modelFailureReason(err error) string {
	var dns *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), errors.As(err, &dns):
		return reasonModelUnreachable
	case errors.As(err, &netErr) && netErr.Timeout():
		return reasonModelTimedOut
	case errors.Is(err, llm.ErrRequestRejected), errors.Is(err, llm.ErrRedirectRejected):
		return reasonModelRejected
	case errors.Is(err, llm.ErrUnusableResponse):
		return reasonModelUnusable
	}
	return reasonModelFailed
}
