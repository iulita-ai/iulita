package transcription

import (
	"bytes"
	"context"
	"os/exec"
)

// transcoding wraps a Provider, converting audio the endpoint may reject
// (Telegram voice notes are OGG/Opus; GLM-ASR accepts only wav/mp3) to
// 16 kHz mono mp3 via ffmpeg. When ffmpeg is unavailable or fails, the
// original bytes pass through unchanged — endpoints that accept the input
// natively (OpenAI, Groq) keep working without it.
type transcoding struct {
	inner  Provider
	ffmpeg string
}

// NewTranscoding wraps p with ffmpeg-based format conversion.
func NewTranscoding(p Provider, ffmpegPath string) Provider {
	return &transcoding{inner: p, ffmpeg: ffmpegPath}
}

// transcodableFrom lists formats assumed acceptable to strict endpoints;
// anything else gets converted when possible.
var directFormats = map[string]bool{
	"mp3": true,
	"wav": true,
}

// Transcribe converts the audio to mp3 when needed and delegates to the
// wrapped provider.
func (t *transcoding) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	if format == "" || directFormats[format] || t.ffmpeg == "" {
		return t.inner.Transcribe(ctx, audio, format)
	}

	// The binary path comes from exec.LookPath and arguments are constants;
	// audio flows through stdin/stdout pipes, never through the shell.
	cmd := exec.CommandContext(ctx, t.ffmpeg, //nolint:gosec // fixed binary, constant args, piped IO
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-ar", "16000", "-ac", "1", "-b:a", "48k",
		"-f", "mp3", "pipe:1",
	)
	var out, errBuf bytes.Buffer
	cmd.Stdin = bytes.NewReader(audio)
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// Best-effort: pass the original through and let the endpoint decide.
		return t.inner.Transcribe(ctx, audio, format)
	}
	return t.inner.Transcribe(ctx, out.Bytes(), "mp3")
}

// FindFFmpeg returns the ffmpeg binary path or an empty string.
func FindFFmpeg() string {
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path
	}
	return ""
}
