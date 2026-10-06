package llm

// UnsentRequestError is only for a proven local rejection before HTTP
// submission. Cause must be a coded, sanitized local validation error.
// HTTP errors and transport failures never establish a known zero charge.
type UnsentRequestError struct{ Cause error }

func (e *UnsentRequestError) Error() string {
	if e.Cause == nil {
		return "request rejected before submission"
	}
	return "request rejected before submission: " + e.Cause.Error()
}

// NoCharge reports that local validation rejected the request before submission.
func (e *UnsentRequestError) NoCharge() bool { return true }
func (e *UnsentRequestError) Unwrap() error  { return e.Cause }
