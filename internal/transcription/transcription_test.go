package transcription

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
