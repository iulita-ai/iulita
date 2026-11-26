package transcription

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type transcriptionRoundTrip func(*http.Request) (*http.Response, error)

func (f transcriptionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBusinessTranscriptionRechecksAuthorityAtHTTPAdmission(t *testing.T) {
	calls := 0
	denied := errors.New("revoked")
	provider := NewOpenAI("private-key", "fixture-model", "https://transcription.invalid/v1", &http.Client{Transport: transcriptionRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })})
	ctx := WithRequestGuard(context.Background(), func(context.Context) error { return denied })
	if _, err := provider.Transcribe(ctx, []byte("OggSfixture"), "ogg"); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("revoked source reached STT provider")
	}
}
func TestBusinessTranscriptionNeverFollowsRedirectOrShowsProviderBody(t *testing.T) {
	calls := 0
	provider := NewOpenAI("private-key", "fixture-model", "https://transcription.invalid/v1", &http.Client{Transport: transcriptionRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://other.invalid/private"}}, Body: io.NopCloser(strings.NewReader("PRIVATE PROVIDER BODY"))}, nil
	})})
	ctx := WithRequestGuard(context.Background(), func(context.Context) error { return nil })
	_, err := provider.Transcribe(ctx, []byte("OggSfixture"), "ogg")
	if err == nil || strings.Contains(err.Error(), "PRIVATE") || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestBusinessTranscriptionBoundsResponseAndFingerprintsConsent(t *testing.T) {
	provider := NewOpenAI("private-key", "fixture-model", "https://transcription.invalid/private-path", &http.Client{Transport: transcriptionRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1)))}, nil
	})})
	ctx := WithRequestGuard(context.Background(), func(context.Context) error { return nil })
	if _, err := provider.Transcribe(ctx, []byte("OggSfixture"), "ogg"); err == nil {
		t.Fatal("oversized transcript accepted")
	}
	label := provider.BusinessDescriptor()
	if label == "" || strings.Contains(label, "private-key") || strings.Contains(label, "private-path") {
		t.Fatal("descriptor disclosed secret or missing")
	}
	other := NewOpenAI("new-key", "fixture-model", "https://transcription.invalid/private-path", nil)
	if label == other.BusinessDescriptor() {
		t.Fatal("credential replacement did not invalidate media consent")
	}
}
func TestBusinessTranscodingRevocationPreventsLocalExecutionAndProviderCall(t *testing.T) {
	calls := 0
	provider := NewTranscoding(ProviderFunc(func(context.Context, []byte, string) (string, error) { calls++; return "", nil }), "/must-not-execute")
	ctx := WithRequestGuard(context.Background(), func(context.Context) error { return errors.New("paused") })
	if _, err := provider.Transcribe(ctx, []byte("OggSfixture"), "ogg"); err == nil || calls != 0 {
		t.Fatal("revoked transcription admitted")
	}
}

func TestGuardedMP3CannotBypassTheDurationLimiter(t *testing.T) {
	calls := 0
	provider := NewTranscoding(ProviderFunc(func(context.Context, []byte, string) (string, error) { calls++; return "unsafe direct upload", nil }), "/nonexistent-business-ffmpeg")
	ctx := WithRequestGuard(context.Background(), func(context.Context) error { return nil })
	if _, err := provider.Transcribe(ctx, []byte("ID3fixture"), "mp3"); err == nil || calls != 0 {
		t.Fatal("MP3 bypassed guarded duration conversion")
	}
	unbounded := NewTranscoding(ProviderFunc(func(context.Context, []byte, string) (string, error) { return "", nil }), "")
	if bounded, ok := unbounded.(interface{ BusinessDurationBounded() bool }); !ok || bounded.BusinessDurationBounded() {
		t.Fatal("missing duration limiter advertised as safe")
	}
}

func TestBusinessPCMRejectsOversizedAudioInsteadOfSilentlyClipping(t *testing.T) {
	wave := func(samples int) []byte {
		header := [...]byte{'R', 'I', 'F', 'F', 0xff, 0xff, 0xff, 0xff, 'W', 'A', 'V', 'E', 'f', 'm', 't', ' ', 16, 0, 0, 0, 1, 0, 1, 0, 0x80, 0x3e, 0, 0, 0, 0x7d, 0, 0, 2, 0, 16, 0, 'd', 'a', 't', 'a', 0xff, 0xff, 0xff, 0xff}
		data := make([]byte, len(header)+samples*2)
		copy(data, header[:])
		return data
	}
	if !validBusinessWAV(wave(120 * 16000)) {
		t.Fatal("complete maximum-duration audio rejected")
	}
	if validBusinessWAV(wave(120*16000 + 1)) {
		t.Fatal("audio exceeding 120 seconds would be silently clipped")
	}
	malformed := wave(1)
	binary.LittleEndian.PutUint32(malformed[16:20], 0xfffffffe)
	if validBusinessWAV(malformed) {
		t.Fatal("overflowing WAV chunk length accepted")
	}
	if validBusinessWAV([]byte("untrusted RIFF bytes")) {
		t.Fatal("malformed PCM accepted")
	}
}

func TestGuardedTranscodingProducesCompleteBoundedWAVWhenAvailable(t *testing.T) {
	ffmpeg := FindFFmpeg()
	if ffmpeg == "" {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	generator := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.1", "-c:a", "libvorbis", "-f", "ogg", "pipe:1")
	var input bytes.Buffer
	generator.Stdout = &input
	if err := generator.Run(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := NewTranscoding(ProviderFunc(func(_ context.Context, data []byte, format string) (string, error) {
		calls++
		if format != "wav" || !validBusinessWAV(data) {
			t.Error("complete guarded WAV not produced")
		}
		return "scoped text", nil
	}), ffmpeg)
	guarded := WithRequestGuard(ctx, func(context.Context) error { return nil })
	if text, err := provider.Transcribe(guarded, input.Bytes(), "ogg"); err != nil || text != "scoped text" || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
