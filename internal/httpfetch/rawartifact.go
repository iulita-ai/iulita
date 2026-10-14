package httpfetch

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// rawArtifactDoc is the versioned owner-private copy of one response: the
// FULL received headers/trailers (unmasked — that is its purpose), request
// metadata without any auth material, and ONLY the already-read bounded
// body. Enabling the option never triggers additional body reading
// (plan §8.2). The doc is stored as a sensitive artifact; it is never
// inlined into the model context.
type rawArtifactDoc struct {
	Version       int                 `json:"version"`
	OperationID   string              `json:"operation_id"`
	StepID        string              `json:"step_id"`
	Method        string              `json:"method"`
	URL           string              `json:"url"` // sanitized (secret query values redacted)
	Status        int                 `json:"status"`
	Protocol      string              `json:"protocol,omitempty"`
	Headers       map[string][]string `json:"headers"` // full, exact values
	Trailers      map[string][]string `json:"trailers,omitempty"`
	BodyComplete  bool                `json:"body_complete"`
	BytesRead     int64               `json:"bytes_read"`
	BytesDeclared int64               `json:"bytes_declared"`
	Body          *rawBody            `json:"body,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
}

type rawBody struct {
	Encoding  string `json:"encoding,omitempty"` // utf-8 | binary | unsupported:<cs>
	MIME      string `json:"mime,omitempty"`
	Text      string `json:"text,omitempty"`      // bounded entity as decoded text
	Base64    string `json:"base64,omitempty"`    // bounded entity, opaque
	Truncated bool   `json:"truncated,omitempty"` // body larger than the inline read
}

// storeRawArtifact persists the raw response copy as a sensitive artifact
// and returns its view for the step result. Delivery/storage are checked
// before use; failures surface as a bounded note, never as extra reads.
func (e *Engine) storeRawArtifact(ctx context.Context, step Request, sr *StepResult, resp *http.Response, ino inlineOutcome, scope Scope, opID string) {
	if !e.rawArtifacts || e.artifacts == nil {
		// Option requested but raw artifacts are not available: report
		// explicitly instead of silently skipping.
		sr.Error = &ErrorInfo{Outcome: sr.Outcome, Code: "raw_artifact_unavailable",
			Message: "include_raw_artifact was requested but raw artifacts are disabled or no artifact store is configured"}
		return
	}
	if scope.UserID == "" {
		sr.Error = &ErrorInfo{Outcome: sr.Outcome, Code: "raw_artifact_unavailable",
			Message: "include_raw_artifact requires an authenticated owner"}
		return
	}

	doc := rawArtifactDoc{
		Version:       1,
		OperationID:   opID,
		StepID:        step.ID,
		Method:        sr.Method,
		URL:           sr.URL,
		Status:        sr.Status,
		Protocol:      sr.Protocol,
		Headers:       flattenHeaders(resp.Header),
		Trailers:      flattenHeaders(resp.Trailer),
		BodyComplete:  ino.info.complete,
		BytesRead:     ino.info.bytesRead,
		BytesDeclared: ino.info.bytesDeclared,
		CreatedAt:     timeNow(),
	}
	if ino.view != nil {
		doc.Body = &rawBody{
			Encoding:  ino.view.Encoding,
			MIME:      ino.view.MIME,
			Truncated: ino.view.Truncated,
		}
		if ino.view.Text != "" {
			doc.Body.Text = ino.view.Text
		} else if len(ino.entity) > 0 {
			doc.Body.Base64 = base64.StdEncoding.EncodeToString(ino.entity)
		}
	}

	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		sr.Error = &ErrorInfo{Outcome: sr.Outcome, Code: "raw_artifact_marshal", Message: err.Error()}
		return
	}

	name := "response-" + sanitizeFilename(step.ID, "step") + ".json"
	spec := ArtifactSpec{
		Filename:    name,
		MIME:        "application/json",
		Sensitive:   true,
		OwnerUserID: scope.UserID,
		ChatID:      scope.ChatID,
		MaxBytes:    e.limits.MaxUploadBytes,
	}
	w, err := e.artifacts.Begin(ctx, spec)
	if err != nil {
		sr.Error = &ErrorInfo{Outcome: sr.Outcome, Code: "raw_artifact_storage", Message: err.Error()}
		return
	}
	if _, werr := w.Write(payload); werr != nil {
		_ = w.Abort() //nolint:errcheck // cleanup on failure path
		sr.Error = &ErrorInfo{Outcome: sr.Outcome, Code: "raw_artifact_storage", Message: werr.Error()}
		return
	}
	sum := sha256.Sum256(payload)
	if cerr := w.Commit(hex.EncodeToString(sum[:]), int64(len(payload))); cerr != nil {
		sr.Error = &ErrorInfo{Outcome: sr.Outcome, Code: "raw_artifact_commit", Message: cerr.Error()}
		return
	}
	if sr.Artifact == nil {
		sr.Artifact = &ArtifactView{
			ID:        artifactIDOf(w),
			Filename:  name,
			Bytes:     int64(len(payload)),
			SHA256:    hex.EncodeToString(sum[:]),
			MIME:      "application/json",
			ExpiresAt: timeNow().Add(e.limits.ArtifactTTL),
			Sensitive: true,
		}
	}
}

func flattenHeaders(h http.Header) map[string][]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string][]string, len(h))
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		out[k] = append([]string(nil), h[k]...)
	}
	return out
}
