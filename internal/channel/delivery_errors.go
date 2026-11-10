package channel

import "errors"

// ErrDeliveryPaused means transport refused before issuing a send request.
// Unlike a network timeout, it is definite evidence of no delivery attempt.
var ErrDeliveryPaused = errors.New("delivery paused locally")
