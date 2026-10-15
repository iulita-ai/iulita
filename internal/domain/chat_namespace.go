package domain

import (
	"errors"
	"strings"
)

// MobileChatPrefix reserves the entire namespace, including malformed IDs.
// Legacy APIs must not use UUID validation to decide whether an ID is private.
const MobileChatPrefix = "mobile:"

// ErrMobileNamespace rejects mobile identifiers on legacy chat routes.
var ErrMobileNamespace = errors.New("mobile namespace requires the owned mobile pipeline")

// IsMobileChatID reports whether an identifier belongs to the reserved mobile namespace.
func IsMobileChatID(id string) bool {
	return len(id) >= len(MobileChatPrefix) && strings.EqualFold(id[:len(MobileChatPrefix)], MobileChatPrefix)
}

// CheckLegacyChatID rejects the reserved mobile namespace on legacy paths.
func CheckLegacyChatID(id string) error {
	if IsMobileChatID(id) {
		return ErrMobileNamespace
	}
	return nil
}
