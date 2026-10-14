package httpfetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
)

// bodyInfo carries body counters shared with download mode.
type bodyInfo struct {
	bytesRead     int64
	bytesDeclared int64
	complete      bool
}

// classifyReadError maps body-read failures to outcomes.
func classifyReadError(err error) Outcome {
	switch {
	case errors.Is(err, context.Canceled):
		return OutcomeCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimedOut
	}
	return OutcomeTransportError
}

// idleReader wraps a body with an explicit idle timer: a single blocked Read
// longer than BodyIdleTimeout cancels the derived context (plan §11 —
// http.Client.Timeout is not an idle timeout).
type idleReader struct {
	r       io.Reader
	idle    time.Duration
	onStall func()
}

func (ir *idleReader) Read(p []byte) (int, error) {
	type readResult struct {
		n   int
		err error
	}
	done := make(chan readResult, 1)
	timer := time.NewTimer(ir.idle)
	defer timer.Stop()
	go func() {
		n, err := ir.r.Read(p)
		done <- readResult{n, err}
	}()
	select {
	case res := <-done:
		if res.err == nil || errors.Is(res.err, io.EOF) {
			return res.n, res.err
		}
		return res.n, res.err
	case <-timer.C:
		if ir.onStall != nil {
			ir.onStall()
		}
		return 0, fmt.Errorf("body idle timeout after %s", ir.idle)
	}
}

// inlineOutcome bundles everything the step loop needs from an inline read.
type inlineOutcome struct {
	view     *BodyView
	entity   []byte // decoded entity bytes (bounded by the inline cap)
	info     bodyInfo
	trailers []HeaderView
	err      error
}

// readInline reads a bounded inline body: entity bytes capped by
// min(server, request) limits, gzip decoded with separate wire/decoded caps,
// charset decoded via x/text with an explicit unsupported marker, and a
// boundary-safe preview for the LLM.
func (e *Engine) readInline(ctx context.Context, step Request, resp *http.Response) inlineOutcome {
	out := inlineOutcome{info: bodyInfo{bytesDeclared: contentLengthOf(resp)}}

	// No-body statuses and HEAD: never read.
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified || resp.Request.Method == http.MethodHead {
		closeBody(resp)
		out.info.complete = true
		return out
	}

	inlineMax := e.limits.InlineMaxBytes
	if step.Response != nil && step.Response.InlineMaxBytes > 0 && step.Response.InlineMaxBytes < inlineMax {
		inlineMax = step.Response.InlineMaxBytes
	}

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wireCount := &countingReader{inner: resp.Body}

	contentEnc := strings.ToLower(strings.TrimSpace(firstHeaderValue(resp.Header, "Content-Encoding")))
	switch contentEnc {
	case "gzip", "x-gzip":
		// Read the compressed entity bounded, then decompress bounded — both
		// caps sit far below the consent threshold.
		wireBuf, err := readAllBounded(readCtx, wireCount, inlineMax, e.limits.BodyIdleTimeout, cancel)
		if err != nil {
			closeBody(resp)
			out.err = err
			out.info.bytesRead = wireCount.n
			return out
		}
		gz, err := gzip.NewReader(bytes.NewReader(wireBuf))
		if err != nil {
			closeBody(resp)
			out.info.bytesRead = wireCount.n
			out.err = fmt.Errorf("gzip response: %w", err)
			return out
		}
		decoded, err := readAllBounded(readCtx, gz, inlineMax, e.limits.BodyIdleTimeout, cancel)
		closeBody(resp)
		out.info.bytesRead = int64(len(decoded))
		out.info.complete = int64(len(wireBuf)) < inlineMax
		if err != nil {
			out.err = err
			return out
		}
		out.entity = decoded
		out.view = e.renderBody(resp, decoded, out.info)
		out.trailers = trailerViews(resp)
		return out
	case "", "identity":
		// plain read below
	default:
		// Unknown content-encoding: keep raw bytes opaque.
	}

	buf, err := readAllBounded(readCtx, wireCount, inlineMax, e.limits.BodyIdleTimeout, cancel)
	closeBody(resp)
	out.info.bytesRead = wireCount.n
	if err != nil {
		out.err = err
		return out
	}
	out.entity = buf
	out.info.complete = int64(len(buf)) < inlineMax
	out.view = e.renderBody(resp, buf, out.info)
	out.trailers = trailerViews(resp)
	return out
}

// readAllBounded reads up to max bytes (+1 to detect truncation) under an
// idle timer. Reading limit+1 is safe here: inline caps are far below the
// consent threshold.
func readAllBounded(_ context.Context, r io.Reader, limit int64, idle time.Duration, stallCancel context.CancelFunc) ([]byte, error) {
	ir := &idleReader{r: io.LimitReader(r, limit+1), idle: idle, onStall: stallCancel}
	var buf bytes.Buffer
	tmp := make([]byte, 32*1024)
	for {
		n, err := ir.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buf.Bytes(), nil
			}
			return buf.Bytes(), err
		}
		if int64(buf.Len()) > limit {
			return buf.Bytes(), nil
		}
	}
}

// renderBody builds the LLM-facing body view with preview truncation.
func (e *Engine) renderBody(resp *http.Response, entity []byte, info bodyInfo) *BodyView {
	ct := firstHeaderValue(resp.Header, "Content-Type")
	mediatype, params, _ := mime.ParseMediaType(ct) //nolint:errcheck // malformed Content-Type falls back to binary
	bv := &BodyView{MIME: mediatype, Truncated: !info.complete || int64(len(entity)) > e.limits.BodyPreviewBytes}

	charset := strings.ToLower(strings.TrimSpace(params["charset"]))
	if isTextualMediaType(mediatype) {
		switch charset {
		case "", "utf-8", "us-ascii":
			bv.Encoding = "utf-8"
			bv.Text = truncateUTF8(string(entity), int(e.limits.BodyPreviewBytes))
		default:
			if enc, err := htmlindex.Get(charset); err == nil && enc != nil {
				if decoded, derr := enc.NewDecoder().Bytes(entity); derr == nil && utf8.Valid(decoded) {
					bv.Encoding = charset
					bv.Text = truncateUTF8(string(decoded), int(e.limits.BodyPreviewBytes))
					return bv
				}
			}
			bv.Encoding = "unsupported:" + charset
			bv.Base64 = base64.StdEncoding.EncodeToString(entity[:min64(int64(len(entity)), 2048)])
		}
		return bv
	}
	// Binary: small base64 preview only.
	bv.Encoding = "binary"
	n := min64(int64(len(entity)), 4096)
	bv.Base64 = base64.StdEncoding.EncodeToString(entity[:n])
	return bv
}

// trailerViews projects response trailers (available only after EOF).
func trailerViews(resp *http.Response) []HeaderView {
	if resp.Trailer == nil {
		return nil
	}
	names := make([]string, 0, len(resp.Trailer))
	for k := range resp.Trailer {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []HeaderView
	for _, n := range names {
		out = append(out, headerView(n, resp.Trailer[n]))
	}
	return out
}

// projectHeaders renders the bounded, masked header projection.
func (e *Engine) projectHeaders(resp *http.Response, budget int64) []HeaderView {
	names := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []HeaderView
	used := int64(0)
	for _, n := range names {
		hv := headerView(n, resp.Header[n])
		size := int64(len(n) + 8)
		for _, v := range hv.Values {
			size += int64(len(v) + 8)
		}
		if used+size > budget {
			return append(out, HeaderView{Name: "(headers truncated)", Values: []string{strconv.FormatBool(true)}})
		}
		used += size
		out = append(out, hv)
	}
	return out
}

// contentLengthOf returns the declared Content-Length or -1.
func contentLengthOf(resp *http.Response) int64 {
	if resp.ContentLength > 0 {
		return resp.ContentLength
	}
	if v := firstHeaderValue(resp.Header, "Content-Length"); v != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return -1
}

// isTextualMediaType decides text vs binary rendering.
func isTextualMediaType(mt string) bool {
	mt = strings.ToLower(mt)
	if mt == "" {
		return false
	}
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	switch {
	case strings.HasPrefix(mt, "application/json"),
		strings.HasPrefix(mt, "application/xml"),
		strings.HasPrefix(mt, "application/javascript"),
		strings.HasPrefix(mt, "application/x-www-form-urlencoded"),
		strings.HasPrefix(mt, "application/atom"),
		strings.HasPrefix(mt, "application/rss"),
		strings.HasPrefix(mt, "application/yaml"),
		strings.HasPrefix(mt, "application/csv"),
		strings.HasSuffix(mt, "+json"),
		strings.HasSuffix(mt, "+xml"):
		return true
	}
	return false
}

// truncateUTF8 cuts on a rune boundary, appending an explicit marker.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for cut != "" && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n…(truncated)"
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// countingReader counts bytes that left the network stack.
type countingReader struct {
	inner io.Reader
	n     int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	c.n += int64(n)
	return n, err
}
