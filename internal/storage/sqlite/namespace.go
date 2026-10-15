package sqlite

import (
	"strconv"
	"strings"

	"github.com/iulita-ai/iulita/internal/domain"
)

// Applied before pagination, counts, FTS and ranking. Future owned mobile
// repositories must use separate methods, never an opt-out context value.
var legacyChatPredicate = legacyChatFilter("chat_id")

// column is an internal SQL identifier, never request input. Prefix matching uses
// the same reserved namespace as the channel guard, including malformed IDs.
func legacyChatFilter(column string) string {
	return "lower(substr(" + column + ", 1, " + strconv.Itoa(len(domain.MobileChatPrefix)) + ")) <> '" + strings.ReplaceAll(domain.MobileChatPrefix, "'", "''") + "'"
}
