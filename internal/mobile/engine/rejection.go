package engine

// Rejection is reserved for a tool's proven pre-dispatch failure. An ambiguous
// HTTP/native result must never use it; those terminate as unknown_outcome.
type Rejection struct{}

func (Rejection) Error() string { return "operation unavailable or rejected before dispatch" }
