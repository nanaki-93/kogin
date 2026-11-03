package app

import (
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

// requireRemoteConfirmation checks a scope's runtime the way each request
// that sends context to a model does before dispatch.
func (s *Service) requireRemoteConfirmation(scope config.ModelScope, confirmed bool) error {
	runtime, ok := s.conf().runtimes.forScope(string(scope))
	if !ok {
		return fmt.Errorf("unknown model scope %q", scope)
	}
	return requireModelRuntimeConfirmation(runtime, confirmed)
}
