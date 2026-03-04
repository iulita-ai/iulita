package llm

import "errors"

// ErrContextTooLarge is returned when the LLM refuses the request due to
// context length exceeding its limit. The caller should compress history and retry.
var ErrContextTooLarge = errors.New("context too large")

// IsContextTooLarge returns true if err wraps ErrContextTooLarge.
func IsContextTooLarge(err error) bool {
	return errors.Is(err, ErrContextTooLarge)
}
