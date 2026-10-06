package llm

import "errors"

// ErrContextTooLarge is returned when the LLM refuses the request due to
// context length exceeding its limit. The caller should compress history and retry.
var ErrContextTooLarge = errors.New("context too large")

// IsContextTooLarge returns true if err wraps ErrContextTooLarge.
func IsContextTooLarge(err error) bool {
	return errors.Is(err, ErrContextTooLarge)
}

// ErrIncompleteResponse signals truncated or empty output. Known usage may
// still be present in the returned response and must be accounted for.
var ErrIncompleteResponse = errors.New("incomplete response")

// AvailabilityError exposes a transient transport failure without private URLs
// or proxy credentials. Parent cancellation is kept as the original context error.
type AvailabilityError struct{ Provider string }

func (e *AvailabilityError) Error() string { return e.Provider + " temporarily unavailable" }

// Retryable permits a bounded availability retry before visible output.
func (e *AvailabilityError) Retryable() bool { return true }
