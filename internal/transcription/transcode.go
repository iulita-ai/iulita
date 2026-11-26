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
	if err := checkRequestGuard(ctx); err != nil {
		return "", err
	}
	if hasRequestGuard(ctx) && t.ffmpeg == "" {
		return "", errGuardedTranscription
	}
	if !hasRequestGuard(ctx) && (format == "" || directFormats[format] || t.ffmpeg == "") {
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
	if hasRequestGuard(ctx) {
		// A 121-second sentinel detects oversized source audio. Reject it before
		// upload instead of silently sending a truncated first 120 seconds.
		cmd.Args = []string{t.ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-protocol_whitelist", "pipe", "-i", "pipe:0", "-t", "121", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", "-f", "wav", "pipe:1"}
		cmd.Env = []string{"LANG=C", "LC_ALL=C"}
		out := &boundedAudioOutput{limit: (121 * 16000 * 2) + 65536}
		diagnostics := &boundedAudioOutput{limit: 4096}
		cmd.Stdin = bytes.NewReader(audio)
		cmd.Stdout, cmd.Stderr = out, diagnostics
		if err := cmd.Run(); err != nil || out.exceeded || diagnostics.exceeded {
			return "", errGuardedTranscription
		}
		if !validBusinessWAV(out.data) {
			return "", errGuardedTranscription
		}
		if err := checkRequestGuard(ctx); err != nil {
			return "", err
		}
		return t.inner.Transcribe(ctx, out.data, "wav")
	}
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
