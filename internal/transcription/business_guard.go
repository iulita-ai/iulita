package transcription

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type requestGuardKey struct{}

// WithRequestGuard carries trusted local authority, never peer-provided data.
// Guarded transcription makes one HTTP attempt and rejects all redirects.
func WithRequestGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, requestGuardKey{}, guard)
}
func hasRequestGuard(ctx context.Context) bool {
	guard, ok := ctx.Value(requestGuardKey{}).(func(context.Context) error)
	return ok && guard != nil
}
func checkRequestGuard(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard, ok := ctx.Value(requestGuardKey{}).(func(context.Context) error); ok && guard != nil {
		return guard(ctx)
	}
	return nil
}

type guardedTransport struct{ base http.RoundTripper }

// RoundTrip checks current authority at each guarded HTTP admission.
func (t guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := checkRequestGuard(request.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(request)
}
func requestClient(client *http.Client, ctx context.Context) *http.Client {
	if !hasRequestGuard(ctx) {
		return client
	}
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	transport := cloned.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	cloned.Transport = guardedTransport{base: transport}
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cloned
}

// BusinessDescriptor identifies consented STT configuration without exposing its
// endpoint path or credential. A provider/model/credential change changes it.
func (o *OpenAI) BusinessDescriptor() string {
	parsed, err := url.Parse(o.baseURL)
	if err != nil || parsed.Hostname() == "" || o.model == "" || len(o.model) > 64 {
		return ""
	}
	model := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, o.model)
	if model != o.model {
		return ""
	}
	hash := sha256.Sum256([]byte(o.baseURL + "\x00" + o.model + "\x00" + o.apiKey))
	return "stt:" + parsed.Hostname() + "/" + model + "#" + hex.EncodeToString(hash[:8])
}

// BusinessDescriptor includes the configured STT identity and local conversion version.
func (t *transcoding) BusinessDescriptor() string {
	provider, ok := t.inner.(interface{ BusinessDescriptor() string })
	if !ok {
		return ""
	}
	value := provider.BusinessDescriptor()
	if value == "" {
		return ""
	}
	if t.ffmpeg != "" {
		value += ";local-audio-conversion-v1"
	}
	return value
}

var errGuardedTranscription = errors.New("guarded transcription unavailable")

type boundedAudioOutput struct {
	data     []byte
	limit    int
	exceeded bool
}

func (b *boundedAudioOutput) Write(data []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if len(data) > remaining {
		b.exceeded = true
		return 0, errGuardedTranscription
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

// BusinessDurationBounded is true only for the guarded local duration-limiting wrapper.
func (t *transcoding) BusinessDurationBounded() bool { return t.ffmpeg != "" }

// validBusinessWAV verifies our fixed PCM output and exact complete duration.
// ffmpeg uses an unknown data length for non-seekable stdout; remaining bytes
// are authoritative in that case, under the independent bounded output cap.
func validBusinessWAV(data []byte) bool {
	if len(data) < 12 || len(data) > 4<<20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return false
	}
	format := false
	for offset := 12; offset+8 <= len(data); {
		kind := string(data[offset : offset+4])
		rawSize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		start := offset + 8
		size := 0
		if rawSize == 0xffffffff {
			if kind != "data" {
				return false
			}
			size = len(data) - start
		} else {
			if rawSize > 4<<20 {
				return false
			}
			size = int(rawSize)
		}
		if kind == "data" {
			return format && size > 0 && size <= 120*16000*2 && size%2 == 0 && size == len(data)-start
		}
		if size > len(data)-start {
			return false
		}
		if kind == "fmt " {
			if size < 16 {
				return false
			}
			pcm := data[start : start+16]
			format = binary.LittleEndian.Uint16(pcm[0:2]) == 1 && binary.LittleEndian.Uint16(pcm[2:4]) == 1 && binary.LittleEndian.Uint32(pcm[4:8]) == 16000 && binary.LittleEndian.Uint32(pcm[8:12]) == 32000 && binary.LittleEndian.Uint16(pcm[12:14]) == 2 && binary.LittleEndian.Uint16(pcm[14:16]) == 16
			if !format {
				return false
			}
		}
		offset = start + size + size%2
	}
	return false
}
