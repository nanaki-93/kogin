package app

// onlyUnavailableRequiredChecks reports whether every unmet required check
// could not execute, as opposed to having run and failed.
func onlyUnavailableRequiredChecks(checks []CheckResult) bool {
	for _, check := range checks {
		if check.Required && check.State != CheckPassed && check.State != CheckUnavailable {
			return false
		}
	}
	return true
}
