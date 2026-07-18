package dashboard

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/scheduler/handlers"
)

const (
	// importUploadPath is exempt from the small-body guard (large zip upload).
	importUploadPath = "/api/import/claude-export"

	// defaultMaxBodyBytes restores the pre-existing small-body protection on all
	// non-upload API routes after the global BodyLimit is raised for the upload.
	defaultMaxBodyBytes = 4 << 20 // 4MB

	// maxImportUploadBytes is the owner-set cap on the uploaded zip.
	maxImportUploadBytes = 1 << 30 // 1GB
	// maxImportBodyBytes is the global BodyLimit: the upload cap plus multipart slack.
	maxImportBodyBytes = maxImportUploadBytes + (16 << 20)

	// Uncompressed-size caps (zip-bomb guard), matching the import handler.
	maxImportConvBytes  = 1 << 30 // 1GB
	maxImportMemBytes   = 8 << 20 // 8MB
	maxImportTotalBytes = maxImportConvBytes + maxImportMemBytes + (64 << 20)
	maxImportMembers    = 64

	importTaskKeyPrefix = "import:"

	importConvMember = "conversations.json"
	importMemMember  = "memories.json"
)

// handleImportClaudeExport accepts a multipart zip upload (field "export"), stages it
// to DataDir/imports keyed by content SHA, validates it (zip-bomb guards + required
// members), deduplicates against prior runs, and enqueues a background import task.
func (s *Server) handleImportClaudeExport(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.Role != domain.RoleAdmin {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "admin access required"})
	}
	ctx := c.Context()

	file, err := c.FormFile("export")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no file uploaded (field 'export')"})
	}
	if file.Size > maxImportUploadBytes {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{"error": "export too large (max 1GB)"})
	}
	importMemories := c.FormValue("import_memories", "true") != "false"

	dir := config.ResolvePaths().ImportsDir()
	if err = os.MkdirAll(dir, 0o700); err != nil {
		s.logger.Error("import: creating imports dir", zap.String("dir", dir), zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to stage upload"})
	}

	// Stream the upload to a temp file while hashing it in one pass. stageUpload logs
	// the underlying I/O error; a staging failure is a server error, not a client one.
	dest, sum, err := s.stageUpload(file, dir)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Validate the staged zip; on rejection remove the staged file (no task owns it yet).
	if vErr := validateExportZip(dest); vErr != nil {
		s.discardStaged(dest)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":          vErr.Error(),
			"expected_files": []string{importConvMember, importMemMember},
			"hint":           "Upload the zip from Claude → Settings → Privacy → Export data.",
		})
	}

	jobID := sum // content-addressed: re-uploading the same file maps to the same run.

	// Dedup against prior runs of the same content. NOTE: the staged file is content-
	// addressed (<sha>.zip), so when a task already owns it we must NOT delete it — a
	// concurrent upload's cleanup would otherwise pull the file out from under the
	// running/just-enqueued import.
	if existing, gErr := s.store.GetImportRun(ctx, claims.UserID, jobID); gErr == nil && existing != nil {
		switch existing.Status {
		case "running":
			return c.JSON(fiber.Map{"status": "already_queued", "job_id": jobID, "existing": existing})
		case "done":
			s.discardStaged(dest) // terminal, no task owns the zip
			return c.JSON(fiber.Map{"status": "already_imported", "job_id": jobID, "finished_at": existing.FinishedAt})
		}
		// failed/canceled → allow a fresh run with the newly staged zip.
	}

	payload, mErr := json.Marshal(map[string]any{
		"zip_path":        dest,
		"user_id":         claims.UserID,
		"job_id":          jobID,
		"source_sha":      sum,
		"import_memories": importMemories,
	})
	if mErr != nil {
		s.discardStaged(dest)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to queue import"})
	}

	// Record the run row BEFORE enqueuing so status/cancel are consistent from the
	// moment we return 202 (the worker also upserts it on start, so this is idempotent).
	if err = s.store.UpsertImportRun(ctx, &domain.ImportRun{
		JobID: jobID, UserID: claims.UserID, Status: "running", SourceSHA: sum, StartedAt: time.Now(),
	}); err != nil {
		s.logger.Warn("failed to record import run", zap.String("job_id", jobID), zap.Error(err))
	}

	created, err := s.store.CreateTaskIfNotExists(ctx, &domain.Task{
		Type:         handlers.TaskTypeImportClaudeExport,
		Payload:      string(payload),
		Capabilities: "import",
		MaxAttempts:  1, // no auto-retry storm; crash-resume is via stale-reclaim, manual resume via re-upload
		UniqueKey:    importTaskKeyPrefix + sum,
	})
	if err != nil {
		s.logger.Error("import: enqueue task", zap.String("job_id", jobID), zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to queue import"})
	}
	if !created {
		// A concurrent upload of the same content already enqueued it. Do NOT delete
		// the shared content-addressed zip — the winner's task owns it.
		return c.JSON(fiber.Map{"status": "already_queued", "job_id": jobID})
	}

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"status": "queued", "job_id": jobID})
}

// stageUpload streams the multipart file into a unique temp file in dir while computing
// its SHA-256, then renames it to <sha>.zip (0600). Returns the final path and hex sum.
func (s *Server) stageUpload(file *multipart.FileHeader, dir string) (dest, sum string, err error) {
	src, err := file.Open()
	if err != nil {
		return "", "", fmt.Errorf("reading uploaded file")
	}
	defer func() { _ = src.Close() }() //nolint:errcheck // read-only source close

	tmp, err := os.CreateTemp(dir, "upload-*.tmp")
	if err != nil {
		s.logger.Error("import: creating staging temp file", zap.String("dir", dir), zap.Error(err))
		return "", "", fmt.Errorf("staging upload")
	}
	tmpPath := tmp.Name()
	hasher := sha256.New()
	// LimitReader guards against a Content-Length that understates the real size.
	if _, cErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(src, maxImportUploadBytes+1)); cErr != nil {
		_ = tmp.Close() //nolint:errcheck // best-effort close on the error path
		s.discardStaged(tmpPath)
		s.logger.Error("import: writing staged upload (disk full?)", zap.Error(cErr))
		return "", "", fmt.Errorf("saving upload")
	}
	if err = tmp.Close(); err != nil {
		s.discardStaged(tmpPath)
		s.logger.Error("import: closing staged upload", zap.Error(err))
		return "", "", fmt.Errorf("saving upload")
	}

	sum = hex.EncodeToString(hasher.Sum(nil))
	dest = filepath.Join(dir, sum+".zip")
	if err = os.Rename(tmpPath, dest); err != nil {
		s.discardStaged(tmpPath)
		s.logger.Error("import: finalizing staged upload", zap.Error(err))
		return "", "", fmt.Errorf("finalizing upload")
	}
	if err = os.Chmod(dest, 0o600); err != nil {
		s.logger.Warn("failed to chmod staged import zip", zap.String("path", dest), zap.Error(err))
	}
	return dest, sum, nil
}

// validateExportZip performs synchronous zip-bomb and structure validation before the
// upload is accepted, so a malicious or malformed archive is rejected at upload time.
func validateExportZip(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("not a valid zip archive")
	}
	defer zr.Close() //nolint:errcheck // read-only reader close

	if len(zr.File) > maxImportMembers {
		return fmt.Errorf("archive has too many entries")
	}

	var hasConv bool
	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > maxImportTotalBytes {
			return fmt.Errorf("archive uncompressed size exceeds limit")
		}
		switch f.Name {
		case importConvMember:
			hasConv = true
			if f.UncompressedSize64 == 0 {
				return fmt.Errorf("%s is empty", importConvMember)
			}
			if f.UncompressedSize64 > maxImportConvBytes {
				return fmt.Errorf("%s exceeds size limit", importConvMember)
			}
		case importMemMember:
			if f.UncompressedSize64 > maxImportMemBytes {
				return fmt.Errorf("%s exceeds size limit", importMemMember)
			}
		}
	}
	if !hasConv {
		return fmt.Errorf("export is missing %s", importConvMember)
	}
	return nil
}

// handleImportStatus returns the caller's import runs, newest first.
func (s *Server) handleImportStatus(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication required"})
	}
	runs, err := s.store.ListImportRuns(c.Context(), claims.UserID, 50)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if runs == nil {
		runs = []domain.ImportRun{}
	}
	return c.JSON(runs)
}

// handleImportCancel cancels a not-yet-started import job. A running import cannot be
// canceled here (it runs to completion / aborts via its own heartbeat).
func (s *Server) handleImportCancel(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.Role != domain.RoleAdmin {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "admin access required"})
	}
	jobID := c.Params("job_id")
	ctx := c.Context()

	// Ownership check: only the run's owner may cancel it.
	run, err := s.store.GetImportRun(ctx, claims.UserID, jobID)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if run == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "import not found"})
	}

	canceled, err := s.store.CancelPendingImportTask(ctx, importTaskKeyPrefix+jobID)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if !canceled {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "import already running or finished; cannot cancel"})
	}
	run.Status = "canceled"
	run.FinishedAt = time.Now()
	if err := s.store.UpsertImportRun(ctx, run); err != nil {
		s.logger.Warn("failed to mark import run canceled", zap.String("job_id", jobID), zap.Error(err))
	}
	return c.JSON(fiber.Map{"status": "canceled", "job_id": jobID})
}

// importSearchResult is a lightweight archive search hit (content truncated to a snippet).
type importSearchResult struct {
	MessageID        int64     `json:"message_id"`
	ConversationUUID string    `json:"conversation_uuid"`
	Sender           string    `json:"sender"`
	Snippet          string    `json:"snippet"`
	CreatedAt        time.Time `json:"created_at"`
}

// handleImportSearch searches the caller's isolated archive (FTS + optional vectors).
func (s *Server) handleImportSearch(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication required"})
	}
	query := c.Query("q")
	if query == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "q parameter is required"})
	}
	limit := clampLimit(queryInt(c, "limit", 30))
	ctx := c.Context()

	// Embed the query for hybrid search when an embedder is configured; otherwise the
	// store falls back to FTS-only (queryVec=nil).
	var queryVec []float32
	vectorWeight := 0.0
	if s.embedder != nil {
		if vecs, err := s.embedder.Embed(ctx, []string{query}); err == nil && len(vecs) > 0 {
			queryVec = vecs[0]
			vectorWeight = 0.5
		}
	}

	msgs, err := s.store.SearchImportedMessagesHybrid(ctx, claims.UserID, query, queryVec, limit, vectorWeight)
	if err != nil {
		return s.errorResponse(c, err)
	}
	results := make([]importSearchResult, 0, len(msgs))
	for i := range msgs {
		results = append(results, importSearchResult{
			MessageID:        msgs[i].ID,
			ConversationUUID: msgs[i].ConversationUUID,
			Sender:           msgs[i].Sender,
			Snippet:          snippet(msgs[i].Content, 300),
			CreatedAt:        msgs[i].CreatedAt,
		})
	}
	return c.JSON(fiber.Map{"results": results, "vector_search": queryVec != nil})
}

// handleImportListConversations lists the caller's archived conversations (paginated).
func (s *Server) handleImportListConversations(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication required"})
	}
	limit := clampLimit(queryInt(c, "limit", 50))
	offset := queryInt(c, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	convs, err := s.store.ListImportedConversations(c.Context(), claims.UserID, limit, offset)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if convs == nil {
		convs = []domain.ImportedConversation{}
	}
	return c.JSON(convs)
}

// handleImportConversationMessages returns one archived conversation's messages,
// scoped to the caller (IDOR guard on user_id).
func (s *Server) handleImportConversationMessages(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication required"})
	}
	uuid := c.Params("uuid")
	msgs, err := s.store.GetImportedConversationMessages(c.Context(), claims.UserID, uuid)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if msgs == nil {
		msgs = []domain.ImportedMessage{}
	}
	return c.JSON(msgs)
}

// clampLimit bounds a page size to a sane range so a caller cannot request an
// unbounded result set from the archive.
func clampLimit(n int) int {
	if n <= 0 {
		return 30
	}
	if n > 200 {
		return 200
	}
	return n
}

// discardStaged best-effort removes a staged upload file, logging any non-missing error.
func (s *Server) discardStaged(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.logger.Warn("failed to remove staged import file", zap.String("path", path), zap.Error(err))
	}
}

// snippet returns up to maxRunes runes of s (rune-safe), with an ellipsis when trimmed.
func snippet(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}
