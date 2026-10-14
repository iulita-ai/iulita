package httpfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
)

// downloadResponse streams the body into the artifact store (plan §9). The
// consent threshold is enforced BEFORE reading past it: with a declared
// larger Content-Length the question precedes any body read; with an unknown
// or understated length the stream pauses exactly at the threshold on a
// TryAcquire pending slot and the SAME connection resumes after consent —
// no background drain, no automatic replay of the request.
func (e *Engine) downloadResponse(ctx context.Context, step Request, state *runState, scope Scope, resp *http.Response, origin Origin) (*ArtifactView, bodyInfo, *StepResult) {
	info := bodyInfo{bytesDeclared: contentLengthOf(resp)}

	fail := func(outcome Outcome, code, msg string) (*ArtifactView, bodyInfo, *StepResult) {
		closeBody(resp)
		return nil, info, &StepResult{
			ID: step.ID, Outcome: outcome, ProxyUsed: true,
			Error: &ErrorInfo{Outcome: outcome, Code: code, Message: msg},
		}
	}
	if e.artifacts == nil {
		// Delivery path absent: fail before reading — never report a
		// download the user cannot actually retrieve.
		return fail(OutcomePolicyDenied, "artifact_delivery_unavailable",
			"download requires a configured artifact store and authenticated delivery")
	}
	if scope.UserID == "" {
		return fail(OutcomePolicyDenied, "artifact_owner_required", "download requires an authenticated owner")
	}

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified || resp.Request.Method == http.MethodHead {
		closeBody(resp)
		info.complete = true
		return nil, info, nil
	}

	threshold := e.limits.ConsentThresholdBytes
	hardCap := e.limits.MaxDownloadBytes

	// Pre-read consent for a declared larger size; over the hard cap the
	// download is refused without asking (it can never be allowed).
	if info.bytesDeclared > threshold {
		if info.bytesDeclared > hardCap {
			return fail(OutcomeQuotaExceeded, "download_too_large",
				fmt.Sprintf("declared size %d bytes exceeds the hard cap %d", info.bytesDeclared, hardCap))
		}
		filename := e.downloadName(step, resp)
		preview := fmt.Sprintf("Download %s from %s — declared size %s. Continue?",
			filename, origin, humanBytes(info.bytesDeclared))
		if _, ferr := e.askConsent(ctx, state, scope, ConfirmLargeDownload, preview, hardCap); ferr != nil {
			return nil, info, ferr
		}
	}

	filename := e.downloadName(step, resp)
	spec := ArtifactSpec{
		Filename:    filename,
		MIME:        firstHeaderValue(resp.Header, "Content-Type"),
		ContentEnc:  firstHeaderValue(resp.Header, "Content-Encoding"),
		Sensitive:   true, // response bodies are sensitive by default
		OwnerUserID: scope.UserID,
		ChatID:      scope.ChatID,
		MaxBytes:    hardCap,
	}
	w, err := e.artifacts.Begin(ctx, spec)
	if err != nil {
		return fail(OutcomeQuotaExceeded, "artifact_begin", err.Error())
	}
	abort := func() { _ = w.Abort() } //nolint:errcheck // cleanup on failure path

	// Writer permit before network reading (bounded, cancellable wait).
	select {
	case e.writerSlots <- struct{}{}:
	case <-ctx.Done():
		abort()
		return fail(OutcomeCancelled, "cancelled", "cancelled while waiting for a download slot") //nolint:misspell // contract value
	}
	defer func() { <-e.writerSlots }()

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	asked := info.bytesDeclared > threshold
	limit := hardCap

	hasher := sha256.New()
	var total int64
	buf := make([]byte, 64*1024)
	ir := &idleReader{r: resp.Body, idle: e.limits.BodyIdleTimeout, onStall: cancel}

	for {
		if !asked && total >= threshold {
			// Unknown or understated size: pause the stream exactly at the
			// threshold on a TryAcquire pending slot; if none is free, close
			// and clean up rather than queueing unaccounted responses.
			select {
			case e.pendingSlots <- struct{}{}:
			default:
				abort()
				closeBody(resp)
				return nil, info, &StepResult{
					ID: step.ID, Outcome: OutcomeQuotaExceeded, ProxyUsed: true,
					Error: &ErrorInfo{Outcome: OutcomeQuotaExceeded, Code: "pending_busy",
						Message: "too many paused downloads awaiting confirmation"},
				}
			}
			held := true
			releasePending := func() {
				if held {
					<-e.pendingSlots
					held = false
					e.metrics.PendingReleased()
				}
			}
			e.metrics.PendingHeld()
			decision, ferr := e.askConsent(readCtx, state, scope, ConfirmLargeDownload,
				fmt.Sprintf("Download %s from %s — size unknown or larger than %s. Continue up to %s?",
					filename, origin, humanBytes(threshold), humanBytes(hardCap)),
				hardCap)
			releasePending()
			if ferr != nil {
				abort()
				closeBody(resp)
				return nil, info, ferr
			}
			granted := decision.MaxBytes
			if granted <= 0 || granted > hardCap {
				granted = hardCap
			}
			limit = granted
			asked = true
		}
		if total >= limit {
			abort()
			closeBody(resp)
			return nil, info, &StepResult{
				ID: step.ID, Outcome: OutcomeQuotaExceeded, ProxyUsed: true,
				Error: &ErrorInfo{Outcome: OutcomeQuotaExceeded, Code: "download_limit",
					Message: fmt.Sprintf("download reached its byte limit (%d)", limit)},
			}
		}
		// Per-read budget: before consent, a single Read can never carry the
		// total past the threshold (kernel buffers may hold far more); after
		// consent, past the granted limit.
		budget := limit - total
		if !asked {
			budget = threshold - total
		}
		chunk := buf[:min64(int64(len(buf)), budget)]
		n, rerr := ir.Read(chunk)
		if n > 0 {
			total += int64(n)
			hasher.Write(chunk[:n])
			if _, werr := w.Write(chunk[:n]); werr != nil {
				abort()
				closeBody(resp)
				info.bytesRead = total
				return nil, info, &StepResult{
					ID: step.ID, Outcome: OutcomeQuotaExceeded, ProxyUsed: true,
					Error: &ErrorInfo{Outcome: OutcomeQuotaExceeded, Code: "artifact_write", Message: werr.Error()},
				}
			}
		}
		if rerr != nil {
			closeBody(resp)
			if errors.Is(rerr, io.EOF) {
				break
			}
			abort()
			info.bytesRead = total
			outcome := classifyReadError(rerr)
			return nil, info, &StepResult{
				ID: step.ID, Outcome: outcome, ProxyUsed: true,
				Error: &ErrorInfo{Outcome: outcome, Code: "body_read", Message: rerr.Error()},
			}
		}
	}

	info.bytesRead = total
	info.complete = true
	hash := hex.EncodeToString(hasher.Sum(nil))
	if err := w.Commit(hash, total); err != nil {
		return nil, info, &StepResult{
			ID: step.ID, Outcome: OutcomeTransportError, ProxyUsed: true,
			Error: &ErrorInfo{Outcome: OutcomeTransportError, Code: "artifact_commit", Message: err.Error()},
		}
	}
	e.metrics.ArtifactBytes("stored", total)
	av := &ArtifactView{
		ID:        artifactIDOf(w),
		Filename:  filename,
		Bytes:     total,
		SHA256:    hash,
		MIME:      spec.MIME,
		ExpiresAt: timeNow().Add(e.limits.ArtifactTTL),
		Sensitive: true,
	}
	return av, info, nil
}

// downloadName picks a safe display filename: explicit request name >
// Content-Disposition filename > a generic name. Path separators and control
// characters are stripped; this is metadata, never a storage path.
func (e *Engine) downloadName(step Request, resp *http.Response) string {
	if step.Response != nil && step.Response.Filename != "" {
		return sanitizeFilename(step.Response.Filename, "")
	}
	if cd := firstHeaderValue(resp.Header, "Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if fn := params["filename"]; fn != "" {
				return sanitizeFilename(fn, "")
			}
		}
	}
	return "download"
}

// artifactIDOf extracts the store-assigned ID from the writer via the
// ArtifactWriterInfo interface when implemented.
func artifactIDOf(w ArtifactWriter) string {
	if wi, ok := w.(interface{ ID() string }); ok {
		return wi.ID()
	}
	return ""
}

func humanBytes(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1f KB", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
