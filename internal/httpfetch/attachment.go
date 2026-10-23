package httpfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// StageAttachment stores trusted, current-turn attachment bytes for a later
// owner-scoped HTTP upload. It performs no network request or credential access.
func (f *Factory) StageAttachment(ctx context.Context, data []byte, filename, mime string) (ArtifactView, error) {
	if !f.Enabled() {
		return ArtifactView{}, fmt.Errorf("http feature disabled")
	}
	e := f.engine
	scope, ok := ScopeFrom(ctx)
	if !ok || scope.UserID == "" || scope.ArtifactBinding() == "" {
		return ArtifactView{}, fmt.Errorf("attachment requires an authenticated owner and conversation")
	}
	if e.artifacts == nil {
		return ArtifactView{}, fmt.Errorf("attachment storage unavailable")
	}
	if len(data) == 0 || int64(len(data)) > e.limits.MaxUploadBytes {
		return ArtifactView{}, fmt.Errorf("attachment must contain 1 to %d bytes", e.limits.MaxUploadBytes)
	}
	if err := ctx.Err(); err != nil {
		return ArtifactView{}, err
	}
	name := sanitizeFilename(filename, "attachment.bin")
	w, err := e.artifacts.Begin(ctx, ArtifactSpec{Filename: name, MIME: mime, Sensitive: true, OwnerUserID: scope.UserID, ChatID: scope.ArtifactBinding(), MaxBytes: int64(len(data))})
	if err != nil {
		return ArtifactView{}, fmt.Errorf("storing attachment: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = w.Abort() //nolint:errcheck // best-effort partial cleanup
		}
	}()
	id := artifactIDOf(w)
	if id == "" {
		return ArtifactView{}, fmt.Errorf("attachment storage did not return a reference")
	}
	n, err := w.Write(data)
	if err != nil {
		return ArtifactView{}, fmt.Errorf("writing attachment: %w", err)
	}
	if n != len(data) {
		return ArtifactView{}, io.ErrShortWrite
	}
	if err := ctx.Err(); err != nil {
		return ArtifactView{}, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if err := w.Commit(hash, int64(n)); err != nil {
		return ArtifactView{}, fmt.Errorf("committing attachment: %w", err)
	}
	committed = true
	return ArtifactView{ID: id, Filename: name, Bytes: int64(n), SHA256: hash, MIME: mime, Sensitive: true}, nil
}
