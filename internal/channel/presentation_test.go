package channel

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

func TestResponsePresentationSurvivesDerivedContextsAndIsRequestLocal(t *testing.T) {
	ctx, presentation := WithResponsePresentation(context.Background())
	other, independent := WithResponsePresentation(ctx)
	if presentation.Verbatim() || independent.Verbatim() {
		t.Fatal("new response presentation must default to ordinary formatting")
	}
	derived, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			MarkVerbatimResponse(derived)
			_ = presentation.Verbatim()
		}()
	}
	workers.Wait()
	if !presentation.Verbatim() || independent.Verbatim() {
		t.Fatal("mark did not stay within the original request")
	}
	MarkVerbatimResponse(other)
	if !independent.Verbatim() {
		t.Fatal("fresh carrier could not be marked")
	}
	MarkVerbatimResponse(context.Background()) // Other channels need no carrier.
}

func TestVerbatimChunksPreserveRawTextAndUTF16Bounds(t *testing.T) {
	for _, input := range []string{
		"",
		"  <tag attr=\"a&b\"> `literal`\n\n# heading\t  \n",
		strings.Repeat("абв العربية עברית 😀👩‍💻\n  ", 1000),
		strings.Repeat("x", 4095) + "😀\n\n " + strings.Repeat("y", 4100),
	} {
		for _, limit := range []int{2, 3000, 4096} {
			chunks := VerbatimChunks(input, limit)
			if strings.Join(chunks, "") != input {
				t.Fatal("chunking changed text or whitespace")
			}
			for _, chunk := range chunks {
				if chunk == "" || !utf8.ValidString(chunk) || len(utf16.Encode([]rune(chunk))) > limit {
					t.Fatal("invalid or oversized literal chunk")
				}
			}
		}
	}
}
