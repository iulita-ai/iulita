package telegram

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	_ "image/png" // Decode only locally bounded PNG/JPEG source images.
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/security"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/pdfreader"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"github.com/iulita-ai/iulita/internal/transcription"
	"golang.org/x/image/draw"
)

const businessSourceBytes = 1 << 20
const businessSourceTextBytes = 16 << 10

// BusinessSourceDescriptor describes only the dedicated media path. It grants
// no access to the ordinary assistant, owner facts, tools or other contacts.
func (c *Channel) BusinessSourceDescriptor() string {
	label := "media-v1:JPEG/PNG512;PDF-local;STT-unavailable"
	if provider, ok := c.transcriberSnapshot().(interface {
		BusinessDescriptor() string
		BusinessDurationBounded() bool
	}); ok {
		if descriptor := provider.BusinessDescriptor(); descriptor != "" && provider.BusinessDurationBounded() {
			label = "media-v1:JPEG/PNG512;PDF-local;" + descriptor
		}
	}
	return label
}

type sourceGuardedTransport struct{ base http.RoundTripper }

// RoundTrip revalidates source authority before transport admission.
func (t sourceGuardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := business.CheckSourceGuard(request.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(request)
}
func (c *Channel) businessSourceClient() *http.Client {
	client := c.http
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	transport := cloned.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	cloned.Transport = sourceGuardedTransport{base: transport}
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cloned.Timeout == 0 || cloned.Timeout > 10*time.Second {
		cloned.Timeout = 10 * time.Second
	}
	return &cloned
}
func (c *Channel) downloadBusinessSource(ctx context.Context, ref business.Media) ([]byte, error) {
	if ref.FileID == "" || len(ref.FileID) > 512 || ref.Bytes < 0 || ref.Bytes > businessSourceBytes {
		return nil, business.ErrQuota
	}
	if err := business.CheckSourceGuard(ctx); err != nil {
		return nil, err
	}
	var file struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := c.businessCall(ctx, "getFile", map[string]any{"file_id": ref.FileID}, &file); err != nil {
		return nil, business.ErrUnavailable
	}
	if file.FileSize < 0 || file.FileSize > businessSourceBytes || !safeBusinessFilePath(file.FilePath) {
		return nil, business.ErrDenied
	}
	if err := business.CheckSourceGuard(ctx); err != nil {
		return nil, err
	}
	base := "https://api.telegram.org/file/bot" + c.bot.Token() + "/"
	if c.testEnvironment {
		base += "test/"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+file.FilePath, http.NoBody)
	if err != nil {
		return nil, business.ErrUnavailable
	}
	response, err := c.businessSourceClient().Do(request)
	if err != nil {
		return nil, business.ErrUnavailable
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil && c.logger != nil {
			c.logger.Debug("Telegram business source response close failed")
		}
	}()
	if response.StatusCode != http.StatusOK || response.ContentLength > businessSourceBytes {
		return nil, business.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, businessSourceBytes+1))
	if err != nil || len(data) == 0 || len(data) > businessSourceBytes {
		return nil, business.ErrQuota
	}
	if err := business.CheckSourceGuard(ctx); err != nil {
		return nil, err
	}
	return data, nil
}
func safeBusinessFilePath(value string) bool {
	if value == "" || len(value) > 512 || strings.HasPrefix(value, "/") || path.Clean(value) != value || strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("_-/.", r):
		default:
			return false
		}
	}
	return true
}

// ProcessBusinessSources uses only the newest incoming attachment group. Older
// attachments are explicitly counted as omitted; an oversized album is rejected
// as a whole. The supplied references are revalidated against this exact peer.
func (c *Channel) ProcessBusinessSources(ctx context.Context, scope business.Scope, messages []business.Message) (business.ProcessedSource, error) {
	return c.processBusinessSources(ctx, scope, messages, pdfreader.Extract)
}
func (c *Channel) processBusinessSources(ctx context.Context, scope business.Scope, messages []business.Message, extract func(context.Context, skill.DocumentAttachment) (*pdfreader.Document, error)) (business.ProcessedSource, error) {
	var result business.ProcessedSource
	if c.business == nil || c.bot == nil || scope.Key.InstanceID != c.instanceID || scope.Key.BotID != c.botID || scope.PeerID <= 0 {
		return result, business.ErrDenied
	}
	if err := business.CheckSourceGuard(ctx); err != nil {
		return result, err
	}
	actual, err := c.business.Messages(ctx, scope)
	if err != nil {
		return result, err
	}
	var latest *business.Message
	for index := range messages {
		if !messages[index].Deleted && messages[index].Direction == "incoming" {
			latest = &messages[index]
		}
	}
	if latest == nil {
		return result, nil
	}
	descriptor := c.BusinessSourceDescriptor()
	cached := func(message *business.Message) bool {
		// Summary projection may remove derived fields from the prompt message.
		// The retained exact source remains the cache authority in that case.
		for index := range actual {
			source := &actual[index]
			if source.ID == message.ID && source.Revision == message.Revision && !source.Deleted && source.ExtractedProvider == descriptor && source.ExtractedText != "" {
				return true
			}
		}
		return false
	}
	selected := []business.Message{}
	for i := range messages {
		message := &messages[i]
		if message.Deleted || len(message.Media) == 0 {
			continue
		}
		inGroup := message == latest || latest.MediaGroupID != "" && message.MediaGroupID == latest.MediaGroupID && message.Direction == "incoming"
		if !inGroup {
			for _, ref := range message.Media {
				if !cached(message) || !textualBusinessMedia(ref) {
					result.Omitted++
				}
			}
			continue
		}
		found := false
		for index := range actual {
			source := &actual[index]
			if source.ID == message.ID && source.Revision == message.Revision && !source.Deleted && source.Direction == "incoming" && sameBusinessMedia(source.Media, message.Media) {
				found = true
				break
			}
		}
		if !found {
			return result, business.ErrDenied
		}
		selected = append(selected, *message)
	}
	items := 0
	for index := range selected {
		message := &selected[index]
		items += len(message.Media)
	}
	if items > 3 {
		return result, business.ErrQuota
	}
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	provider := c.transcriberSnapshot()
	for index := range selected {
		message := &selected[index]
		for _, ref := range message.Media {
			if cached(message) && textualBusinessMedia(ref) {
				continue
			}
			if err := business.CheckSourceGuard(work); err != nil {
				return result, err
			}
			if ref.Kind == "voice" || ref.Kind == "audio" {
				sttBinding, configured := provider.(interface {
					BusinessDescriptor() string
					BusinessDurationBounded() bool
				})
				if !configured || sttBinding.BusinessDescriptor() == "" || !sttBinding.BusinessDurationBounded() {
					return result, business.ErrUnsupported
				}
				if ref.DurationSeconds <= 0 || ref.DurationSeconds > 120 {
					return result, business.ErrQuota
				}
			}
			if ref.Kind != "photo" && ref.Kind != "document" && ref.Kind != "voice" && ref.Kind != "audio" {
				return result, business.ErrUnsupported
			}
			data, err := c.downloadBusinessSource(work, ref)
			if err != nil {
				return result, err
			}
			if err := business.CheckSourceGuard(work); err != nil {
				return result, err
			}
			prefix := "\n[Untrusted " + ref.Kind + " source from incoming message " + strconv.Itoa(message.ID) + "]\n"
			switch ref.Kind {
			case "photo":
				picture, err := boundedBusinessImage(data)
				if err != nil {
					return result, err
				}
				result.Images = append(result.Images, picture)
				result.Text += prefix + "Image attached below.\n"
			case "document":
				var text string
				switch {
				case bytes.HasPrefix(data, []byte("%PDF-")):
					parseCtx, stop := context.WithTimeout(work, 5*time.Second)
					if err := business.CheckSourceGuard(parseCtx); err != nil {
						stop()
						return result, err
					}
					document, err := extract(parseCtx, skill.DocumentAttachment{Data: data, MimeType: "application/pdf", Filename: "business-document.pdf"})
					stop()
					if err != nil || document == nil {
						return result, business.ErrUnsupported
					}
					if document.Pages > 20 {
						return result, business.ErrQuota
					}
					text = document.Text
				case utf8.Valid(data) && strings.HasPrefix(http.DetectContentType(data), "text/plain") && !bytes.ContainsRune(data, 0):
					text = string(data)
				default:
					return result, business.ErrUnsupported
				}
				if len(text) > businessSourceTextBytes {
					return result, business.ErrQuota
				}
				redacted := security.RedactSensitiveText(text)
				result.Text += prefix + redacted
				addBusinessExtraction(&result, message, redacted)
			case "voice", "audio":
				sttBinding, ok := provider.(interface {
					BusinessDescriptor() string
					BusinessDurationBounded() bool
				})
				if !ok || sttBinding.BusinessDescriptor() == "" || !sttBinding.BusinessDurationBounded() {
					return result, business.ErrUnsupported
				}
				format := ""
				switch {
				case bytes.HasPrefix(data, []byte("OggS")):
					format = "ogg"
				case bytes.HasPrefix(data, []byte("RIFF")) && len(data) > 12 && string(data[8:12]) == "WAVE":
					format = "wav"
				case bytes.HasPrefix(data, []byte("ID3")) || len(data) > 1 && data[0] == 0xff && data[1]&0xe0 == 0xe0:
					format = "mp3"
				case len(data) > 12 && string(data[4:8]) == "ftyp":
					format = "m4a"
				default:
					return result, business.ErrUnsupported
				}
				if err := business.CheckSourceGuard(work); err != nil {
					return result, err
				}
				if err := business.ReserveSource(work, scope, int64(len(data)), 120); err != nil {
					return result, err
				}
				if err := business.CheckSourceGuard(work); err != nil {
					return result, err
				}
				text, err := provider.Transcribe(transcription.WithRequestGuard(work, business.CheckSourceGuard), data, format)
				if err != nil {
					return result, business.ErrUnavailable
				}
				if !utf8.ValidString(text) || len(text) > businessSourceTextBytes {
					return result, business.ErrQuota
				}
				redacted := security.RedactSensitiveText(text)
				result.Text += prefix + redacted
				addBusinessExtraction(&result, message, redacted)
			}
			if len(result.Text) > businessSourceTextBytes {
				return result, business.ErrQuota
			}
		}
	}
	if err := business.CheckSourceGuard(work); err != nil {
		return result, err
	}
	return result, nil
}
func sameBusinessMedia(a, b []business.Media) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func boundedBusinessImage(data []byte) (business.SourceImage, error) {
	var out business.SourceImage
	if len(data) > businessSourceBytes {
		return out, business.ErrQuota
	}
	configuration, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" && format != "png" {
		return out, business.ErrUnsupported
	}
	if configuration.Width <= 0 || configuration.Height <= 0 || configuration.Width > 4096 || configuration.Height > 4096 || int64(configuration.Width)*int64(configuration.Height) > 4<<20 {
		return out, business.ErrQuota
	}
	if format == "png" {
		for offset := 8; offset+12 <= len(data); {
			size := int(binary.BigEndian.Uint32(data[offset : offset+4]))
			if size < 0 || size > len(data)-offset-12 {
				return out, business.ErrDenied
			}
			if string(data[offset+4:offset+8]) == "acTL" {
				return out, business.ErrUnsupported
			}
			offset += 12 + size
		}
	}
	original, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return out, business.ErrDenied
	}
	width, height := configuration.Width, configuration.Height
	if width > 512 || height > 512 {
		if width >= height {
			height = height * 512 / width
			width = 512
		} else {
			width = width * 512 / height
			height = 512
		}
		if width < 1 {
			width = 1
		}
		if height < 1 {
			height = 1
		}
	}
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.ApproxBiLinear.Scale(destination, destination.Bounds(), original, original.Bounds(), draw.Src, nil)
	var encoded bytes.Buffer
	if jpeg.Encode(&encoded, destination, &jpeg.Options{Quality: 80}) != nil || encoded.Len() > businessSourceBytes {
		return out, business.ErrQuota
	}
	return business.SourceImage{Data: encoded.Bytes(), MediaType: "image/jpeg"}, nil
}

func textualBusinessMedia(ref business.Media) bool {
	return ref.Kind == "document" || ref.Kind == "voice" || ref.Kind == "audio"
}
func addBusinessExtraction(result *business.ProcessedSource, message *business.Message, text string) {
	for i := range result.Extractions {
		if result.Extractions[i].MessageID == message.ID && result.Extractions[i].Revision == message.Revision {
			result.Extractions[i].Text += "\n" + text
			return
		}
	}
	result.Extractions = append(result.Extractions, business.SourceExtraction{MessageID: message.ID, Revision: message.Revision, Text: text})
}
