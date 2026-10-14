package transcription

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestTranscribeCustomBaseURL(t *testing.T) {
	var gotURL, gotModel, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotModel = r.FormValue("model")
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"text":"hello world"}`)
	}))
	defer srv.Close()

	p := NewOpenAI("k-123", "", srv.URL+"/v1/", srv.Client())
	got, err := p.Transcribe(context.Background(), []byte("fake-ogg"), "ogg")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("text = %q", got)
	}
	if gotURL != "/v1/audio/transcriptions" {
		t.Errorf("url = %q, want trailing-slash-stripped /v1/audio/transcriptions", gotURL)
	}
	if gotModel != "whisper-1" {
		t.Errorf("default model = %q, want whisper-1", gotModel)
	}
	if gotAuth != "Bearer k-123" {
		t.Errorf("auth = %q", gotAuth)
	}
}

func TestTranscribeSizeCap(t *testing.T) {
	p := NewOpenAI("k", "m", "", http.DefaultClient)
	big := make([]byte, MaxAudioBytes+1)
	if _, err := p.Transcribe(context.Background(), big, "ogg"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected size-cap error, got %v", err)
	}
}

func TestTranscodingConvertsOggToMp3(t *testing.T) {
	ff := FindFFmpeg()
	if ff == "" {
		t.Skip("ffmpeg not available")
	}
	// Generate a 0.5s 440Hz ogg via ffmpeg, then verify the wrapper hands
	// the inner provider mp3 bytes.
	src := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-c:a", "libvorbis", "-f", "ogg", "pipe:1")
	var ogg bytes.Buffer
	src.Stdout = &ogg
	if err := src.Run(); err != nil {
		t.Fatalf("generating test ogg: %v", err)
	}

	var gotFormat string
	var gotLen int
	inner := ProviderFunc(func(_ context.Context, audio []byte, format string) (string, error) {
		gotFormat, gotLen = format, len(audio)
		return "ok", nil
	})
	wrapped := NewTranscoding(inner, ff)
	if _, err := wrapped.Transcribe(context.Background(), ogg.Bytes(), "ogg"); err != nil {
		t.Fatal(err)
	}
	if gotFormat != "mp3" {
		t.Errorf("format = %q, want mp3", gotFormat)
	}
	if gotLen == 0 {
		t.Error("transcoded audio is empty")
	}
}

// ProviderFunc adapts a function to the Provider interface.
type ProviderFunc func(ctx context.Context, audio []byte, format string) (string, error)

func (f ProviderFunc) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	return f(ctx, audio, format)
}
