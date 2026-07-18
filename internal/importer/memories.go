package importer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/iulita-ai/iulita/internal/domain"
)

// MappedFact pairs a fact to save with its deterministic dedup key. The key is stored
// in the imported_fact_keys sidecar so re-importing an unchanged memory inserts nothing.
type MappedFact struct {
	DedupKey string
	Fact     domain.Fact
}

// headingRe matches a standalone markdown section heading line: "**Heading**" alone on
// its line. The content must contain no "*", which excludes inline bold labels like
// "**Watches & hardware:** ...prose..." that share a line with body text.
var headingRe = regexp.MustCompile(`(?m)^\*\*([^*\n]{1,80})\*\*[ \t]*$`)

// MapMemories maps memories.json (an array of one object) into memory facts. Ordering
// is deterministic: project memories are processed in sorted-UUID order and sections
// in document order, so the fact set and its size are reproducible across runs.
//
// Dedup keys are content/heading-derived and position-independent, so re-importing an
// unchanged memory is a no-op under Phase-2 ON CONFLICT. Two limitations are owned by
// the Phase-2 apply step, not here: (1) a memory whose BODY changed but heading did
// not keeps the same key, so the handler must UPDATE-on-change rather than insert-only
// to avoid stale bodies; (2) sections removed from a later export orphan their facts,
// so the handler may prune keys no longer present. This mapper is pure and only
// produces the (key, fact) pairs.
func MapMemories(data []byte, userID string) ([]MappedFact, error) {
	var arr []dumpMemories
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, fmt.Errorf("unmarshal memories: %w", err)
	}
	var out []MappedFact
	for i := range arr {
		m := &arr[i]
		out = append(out, mapConversationsMemory(m.AccountUUID, m.ConversationsMemory, userID)...)

		uuids := make([]string, 0, len(m.ProjectMemories))
		for k := range m.ProjectMemories {
			uuids = append(uuids, k)
		}
		sort.Strings(uuids)
		for _, projUUID := range uuids {
			out = append(out, mapProjectMemory(projUUID, m.ProjectMemories[projUUID], userID)...)
		}
	}

	// Disambiguate any DedupKey collisions within this import (e.g. two distinct
	// headings that slug to the same value). The first occurrence keeps the stable
	// key; later collisions get a deterministic suffix so Phase-2 ON CONFLICT never
	// silently drops a distinct section. Order is deterministic, so suffixes are too.
	seen := make(map[string]int, len(out))
	for i := range out {
		k := out[i].DedupKey
		if n := seen[k]; n > 0 {
			out[i].DedupKey = k + "-" + strconv.Itoa(n)
		}
		seen[k]++
	}
	return out, nil
}

// mapConversationsMemory splits the global memory into one fact per heading section.
// Sections are not further chunked here (embedding-time chunking is separate); prose
// is preserved whole.
func mapConversationsMemory(accountUUID, text, userID string) []MappedFact {
	sections := splitByHeadings(text)
	out := make([]MappedFact, 0, len(sections))
	for i, s := range sections {
		basis := slug(s.heading)
		if basis == "" {
			basis = "section-" + strconv.Itoa(i)
		}
		out = append(out, MappedFact{
			DedupKey: factKey(accountUUID, "conversations_memory", basis),
			Fact:     memoryFact(userID, formatMemory("[Claude memory]", s.heading, s.body)),
		})
	}
	return out
}

// mapProjectMemory maps one project memory: a single fact when it has no/one heading,
// otherwise one fact per heading section (larger project memories are section-split).
func mapProjectMemory(projUUID, body, userID string) []MappedFact {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	label := "[Claude project memory: " + projUUID + "]"
	sections := splitByHeadings(body)
	if len(sections) <= 1 {
		return []MappedFact{{
			DedupKey: projUUID, // natural key for the whole memory
			Fact:     memoryFact(userID, label+"\n"+body),
		}}
	}
	out := make([]MappedFact, 0, len(sections))
	for i, s := range sections {
		basis := slug(s.heading)
		if basis == "" {
			basis = "section-" + strconv.Itoa(i)
		}
		out = append(out, MappedFact{
			DedupKey: factKey(projUUID, basis),
			Fact:     memoryFact(userID, formatMemory(label, s.heading, s.body)),
		})
	}
	return out
}

func memoryFact(userID, content string) domain.Fact {
	return domain.Fact{
		ChatID:     ImportChatID,
		UserID:     userID,
		Content:    content,
		SourceType: ImportSourceType,
	}
}

func formatMemory(label, heading, body string) string {
	if heading == "" {
		return strings.TrimSpace(label + "\n" + body)
	}
	return strings.TrimSpace(label + " " + heading + "\n" + body)
}

type memorySection struct {
	heading string
	body    string
}

// splitByHeadings splits markdown into sections by standalone "**Heading**" lines.
// Text with no heading yields a single headingless section holding the whole body.
// Any preamble before the first heading becomes its own headingless section.
func splitByHeadings(text string) []memorySection {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	locs := headingRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return []memorySection{{heading: "", body: text}}
	}
	var sections []memorySection
	if pre := strings.TrimSpace(text[:locs[0][0]]); pre != "" {
		sections = append(sections, memorySection{heading: "", body: pre})
	}
	for i, loc := range locs {
		heading := strings.TrimSpace(text[loc[2]:loc[3]])
		bodyEnd := len(text)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}
		body := strings.TrimSpace(text[loc[1]:bodyEnd])
		sections = append(sections, memorySection{heading: heading, body: body})
	}
	return sections
}
