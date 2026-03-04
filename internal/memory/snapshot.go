package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// ExportFacts exports all facts for a chat as markdown.
func ExportFacts(ctx context.Context, store storage.Repository, chatID string) (string, error) {
	facts, err := store.GetAllFacts(ctx, chatID)
	if err != nil {
		return "", fmt.Errorf("getting facts for chat %s: %w", chatID, err)
	}

	var sb strings.Builder
	for _, f := range facts {
		sb.WriteString(fmt.Sprintf("## Fact %d\n%s\n\n", f.ID, f.Content))
	}
	return sb.String(), nil
}

// ExportAllFacts exports all facts across all chats to a directory.
// Each chat gets its own markdown file named by chat ID.
func ExportAllFacts(ctx context.Context, store storage.Repository, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating export directory: %w", err)
	}

	chatIDs, err := store.GetChatIDs(ctx)
	if err != nil {
		return fmt.Errorf("getting chat IDs: %w", err)
	}

	for _, chatID := range chatIDs {
		md, err := ExportFacts(ctx, store, chatID)
		if err != nil {
			return err
		}
		if md == "" {
			continue
		}

		filename := filepath.Join(dir, fmt.Sprintf("%s.md", chatID))
		if err := os.WriteFile(filename, []byte(md), 0o644); err != nil {
			return fmt.Errorf("writing snapshot for chat %s: %w", chatID, err)
		}
	}
	return nil
}

var factHeaderRe = regexp.MustCompile(`(?m)^## Fact \d+\s*$`)

// ImportFacts imports facts from a markdown snapshot.
// Returns the number of facts imported.
func ImportFacts(ctx context.Context, store storage.Repository, chatID string, markdown string) (int, error) {
	// Split by "## Fact {id}" headers.
	indices := factHeaderRe.FindAllStringIndex(markdown, -1)
	if len(indices) == 0 {
		return 0, nil
	}

	count := 0
	for i, loc := range indices {
		// Extract the content between this header and the next (or end of string).
		headerEnd := loc[1]
		var contentEnd int
		if i+1 < len(indices) {
			contentEnd = indices[i+1][0]
		} else {
			contentEnd = len(markdown)
		}

		content := strings.TrimSpace(markdown[headerEnd:contentEnd])
		if content == "" {
			continue
		}

		// Parse the fact ID from the header for logging, but we create new facts.
		header := markdown[loc[0]:loc[1]]
		_ = parseFactID(header) // best-effort parse

		fact := &domain.Fact{
			ChatID:  chatID,
			Content: content,
		}
		if err := store.SaveFact(ctx, fact); err != nil {
			return count, fmt.Errorf("saving imported fact: %w", err)
		}
		count++
	}
	return count, nil
}

func parseFactID(header string) int64 {
	parts := strings.Fields(header)
	if len(parts) >= 3 {
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		return id
	}
	return 0
}
