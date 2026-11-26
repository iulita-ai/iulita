package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/pdfreader"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

func businessSourceFixture(t *testing.T, messages []business.Message, body []byte) (*Channel, business.Scope, *[]string) {
	t.Helper()
	calls := []string{}
	c := businessTestChannel(t, func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.Path)
		if request.URL.Host != "api.telegram.org" {
			t.Error("source left the fixed Telegram origin")
		}
		if strings.HasSuffix(request.URL.Path, "/getFile") {
			return businessHTTPResponse(200, `{"ok":true,"result":{"file_path":"documents/file_1.bin","file_size":`+jsonNumber(len(body))+`}}`), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}, nil
	})
	key := business.Key{InstanceID: c.instanceID, BotID: c.botID, ConnectionID: "source-connection"}
	scope := business.Scope{Key: key, OwnerID: "owner", PeerID: 303}
	state := business.State{Connection: business.Connection{RemoteConnection: business.RemoteConnection{Key: key, Enabled: true, OwnerTelegramID: 42}, OwnerID: "owner", Epoch: 1, RuntimeGeneration: "runtime", VerifiedAt: time.Now(), Policy: business.PeerPolicy{Enabled: true, Consent: true, MediaConsent: true}}, Peers: map[int64]*business.Peer{303: {ID: 303, Revision: 1, Policy: business.PeerPolicy{Enabled: true, Consent: true, MediaConsent: true, Mode: business.ModeDraft}, Messages: messages}}}
	repo := &businessTransportMemory{states: map[business.Key]business.State{key: state}}
	c.business = business.New(repo, nil, nil, business.Options{RuntimeGeneration: "runtime"})
	return c, scope, &calls
}
func jsonNumber(n int) string { data, _ := json.Marshal(n); return string(data) }
func incomingSource(id int, kind string) business.Message {
	return business.Message{ID: id, Revision: 1, Direction: "incoming", Date: time.Now(), Media: []business.Media{{Kind: kind, FileID: "file-reference", Bytes: 128, DurationSeconds: 10}}}
}
func allowedSourceContext() context.Context {
	return business.WithSourceGuard(context.Background(), func(context.Context) error { return nil })
}

func TestBusinessImageSourceIsScopedBoundedAndResized(t *testing.T) {
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 1024, 256))); err != nil {
		t.Fatal(err)
	}
	message := incomingSource(1, "photo")
	c, scope, calls := businessSourceFixture(t, []business.Message{message}, imageBytes.Bytes())
	result, err := c.ProcessBusinessSources(allowedSourceContext(), scope, []business.Message{message})
	if err != nil || len(result.Images) != 1 || len(*calls) != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, len(*calls), err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(result.Images[0].Data))
	if err != nil || format != "jpeg" || config.Width != 512 || config.Height != 128 {
		t.Fatal("source image was not bounded", config, err)
	}
	if !strings.Contains(result.Text, "incoming message 1") {
		t.Fatal("source provenance missing")
	}
}
func TestBusinessMediaMissingGuardAndForgedSourceMakeZeroHTTPCalls(t *testing.T) {
	message := incomingSource(1, "photo")
	c, scope, calls := businessSourceFixture(t, []business.Message{message}, []byte("unused"))
	if _, err := c.ProcessBusinessSources(context.Background(), scope, []business.Message{message}); !errors.Is(err, business.ErrDenied) {
		t.Fatal(err)
	}
	forged := message
	forged.ID = 99
	if _, err := c.ProcessBusinessSources(allowedSourceContext(), scope, []business.Message{forged}); !errors.Is(err, business.ErrDenied) {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatal("untrusted reference fetched")
	}
}
func TestBusinessAlbumRejectsWholeGroupAndCountsOlderOmissions(t *testing.T) {
	var album []business.Message
	for i := 1; i <= 4; i++ {
		m := incomingSource(i, "photo")
		m.MediaGroupID = "same-album"
		album = append(album, m)
	}
	c, scope, calls := businessSourceFixture(t, album, []byte("unused"))
	if _, err := c.ProcessBusinessSources(allowedSourceContext(), scope, album); !errors.Is(err, business.ErrQuota) {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatal("oversized album partly downloaded")
	}
	messages := []business.Message{incomingSource(1, "photo"), incomingSource(2, "document")}
	c, scope, _ = businessSourceFixture(t, messages, []byte("plain document content"))
	result, err := c.ProcessBusinessSources(allowedSourceContext(), scope, messages)
	if err != nil || result.Omitted != 1 || !strings.Contains(result.Text, "plain document content") {
		t.Fatalf("omitted=%d err=%v", result.Omitted, err)
	}
}
func TestBusinessRevocationAfterGetFilePreventsDownloadAndParsing(t *testing.T) {
	message := incomingSource(1, "document")
	c, scope, calls := businessSourceFixture(t, []business.Message{message}, []byte("plain document"))
	ctx := business.WithSourceGuard(context.Background(), func(context.Context) error {
		if len(*calls) > 0 {
			return business.ErrStale
		}
		return nil
	})
	if _, err := c.ProcessBusinessSources(ctx, scope, []business.Message{message}); !errors.Is(err, business.ErrStale) {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatal("revoked source downloaded")
	}
}
func TestBusinessPDFUsesBoundedLocalExtractionAndNoPrivateTool(t *testing.T) {
	message := incomingSource(1, "document")
	c, scope, _ := businessSourceFixture(t, []business.Message{message}, []byte("%PDF-fixture"))
	parsed := false
	result, err := c.processBusinessSources(allowedSourceContext(), scope, []business.Message{message}, func(ctx context.Context, doc skill.DocumentAttachment) (*pdfreader.Document, error) {
		parsed = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || doc.Filename != "business-document.pdf" {
			t.Error("unbounded or arbitrary-path PDF processing")
		}
		return &pdfreader.Document{Pages: 2, Text: "Extracted untrusted text"}, nil
	})
	if err != nil || !parsed || !strings.Contains(result.Text, "Extracted untrusted text") || len(result.Extractions) != 1 || result.Extractions[0].MessageID != message.ID || result.Extractions[0].Revision != message.Revision {
		t.Fatal(result, err)
	}
}
func TestBusinessSourceFilePathsCannotEscapeOriginAndImageBombsAreRejected(t *testing.T) {
	for _, value := range []string{"https://evil.invalid/file", "/etc/passwd", "../file", "photos/../file", "photos/file?token=secret", "photos/file#fragment", "photos\\file"} {
		if safeBusinessFilePath(value) {
			t.Fatalf("accepted %q", value)
		}
	}
	if !safeBusinessFilePath("photos/file_123.jpg") {
		t.Fatal("valid Telegram file path denied")
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 4097, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := boundedBusinessImage(data.Bytes()); !errors.Is(err, business.ErrQuota) {
		t.Fatal("oversized dimensions admitted", err)
	}
}

type businessSourceTranscriber struct{ calls int }

func (*businessSourceTranscriber) BusinessDurationBounded() bool { return true }
func (*businessSourceTranscriber) BusinessDescriptor() string    { return "stt-fixture" }
func (p *businessSourceTranscriber) Transcribe(ctx context.Context, data []byte, format string) (string, error) {
	p.calls++
	if err := business.CheckSourceGuard(ctx); err != nil {
		return "", err
	}
	if !bytes.HasPrefix(data, []byte("OggS")) || format != "ogg" {
		return "", errors.New("unexpected input")
	}
	return "Scoped transcript", nil
}
func TestBusinessAudioUsesOnlyTheExplicitGuardedTranscriber(t *testing.T) {
	message := incomingSource(1, "voice")
	c, scope, calls := businessSourceFixture(t, []business.Message{message}, []byte("OggSfixture"))
	provider := &businessSourceTranscriber{}
	c.SetTranscriber(provider)
	budget := &businessSourceBudgetFixture{}
	if _, err := c.ProcessBusinessSources(allowedSourceContext(), scope, []business.Message{message}); err == nil || provider.calls != 0 {
		t.Fatal("missing source budget admitted STT", err)
	}
	*calls = nil
	ctx := business.WithSourceBudget(allowedSourceContext(), budget)
	result, err := c.ProcessBusinessSources(ctx, scope, []business.Message{message})
	if len(budget.requests) != 1 || budget.requests[0].DurationSeconds != 120 || budget.requests[0].Bytes != int64(len("OggSfixture")) {
		t.Fatal("STT worst-case usage was not reserved", budget.requests)
	}
	if err != nil || provider.calls != 1 || len(*calls) != 2 || !strings.Contains(result.Text, "Scoped transcript") {
		t.Fatal(result, err)
	}
	if !strings.Contains(c.BusinessSourceDescriptor(), "stt-fixture") {
		t.Fatal("STT consent descriptor missing")
	}
	message.Media[0].DurationSeconds = 121
	c, scope, calls = businessSourceFixture(t, []business.Message{message}, []byte("OggSfixture"))
	c.SetTranscriber(provider)
	if _, err := c.ProcessBusinessSources(allowedSourceContext(), scope, []business.Message{message}); !errors.Is(err, business.ErrQuota) || len(*calls) != 0 {
		t.Fatal("oversized voice fetched", err)
	}
}

type businessSourceBudgetFixture struct {
	requests []business.SourceBudgetRequest
}

func (b *businessSourceBudgetFixture) ReserveSource(_ context.Context, request business.SourceBudgetRequest) error {
	b.requests = append(b.requests, request)
	return nil
}

func TestCachedTextSourceIsNotRefetchedOrCountedAsOmitted(t *testing.T) {
	message := incomingSource(1, "document")
	prototype := &Channel{}
	message.ExtractedProvider = prototype.BusinessSourceDescriptor()
	message.ExtractedText = "Saved scoped text"
	c, scope, calls := businessSourceFixture(t, []business.Message{message}, []byte("must-not-fetch"))
	result, err := c.ProcessBusinessSources(allowedSourceContext(), scope, []business.Message{message})
	if err != nil || len(*calls) != 0 || result.Omitted != 0 || len(result.Extractions) != 0 {
		t.Fatal("cached source was refetched", result, err)
	}
	projection := message
	projection.ExtractedText, projection.ExtractedProvider = "", ""
	if _, err := c.ProcessBusinessSources(allowedSourceContext(), scope, []business.Message{projection}); err != nil || len(*calls) != 0 {
		t.Fatal("summarized cache projection was refetched", err)
	}
}

func TestBusinessEmptyOrOutgoingOnlySourceHasNoLatestIncomingIndex(t *testing.T) {
	for _, messages := range [][]business.Message{nil, {{ID: 1, Direction: "owner_outgoing", Date: time.Now()}}} {
		c, scope, calls := businessSourceFixture(t, messages, []byte("unused"))
		if _, err := c.ProcessBusinessSources(allowedSourceContext(), scope, messages); err != nil || len(*calls) != 0 {
			t.Fatal("non-incoming source fetched", err)
		}
	}
}
