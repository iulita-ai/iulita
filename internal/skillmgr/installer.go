package skillmgr

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxExtractedFiles   = 500
	maxSingleFileSize   = 10 << 20 // 10 MB
	maxTotalExtractSize = 50 << 20 // 50 MB
)

// ExtractZIP safely extracts a ZIP archive to destDir with security checks.
// It rejects paths containing "..", absolute paths, and enforces size limits.
func ExtractZIP(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	if len(r.File) > maxExtractedFiles {
		return fmt.Errorf("zip contains %d files, max %d", len(r.File), maxExtractedFiles)
	}

	var totalSize int64
	for _, f := range r.File {
		name := filepath.Clean(f.Name)

		// Security: reject path traversal and absolute paths.
		if strings.Contains(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("unsafe path in zip: %q", f.Name)
		}

		// Strip single top-level directory if all files share it.
		// e.g. "weather-brief-1.0.0/SKILL.md" → "SKILL.md"
		name = stripTopLevel(name, r.File)

		target := filepath.Join(destDir, name)

		// Verify the target path is still within destDir (belt-and-suspenders).
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) &&
			filepath.Clean(target) != filepath.Clean(destDir) {
			return fmt.Errorf("zip path escapes destination: %q", f.Name)
		}

		// Security: reject symlinks to prevent symlink attacks (e.g. Docker volume mount escape).
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("zip contains symlink %q: not allowed", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("create dir %q: %w", target, err)
			}
			continue
		}

		// Size check.
		if f.UncompressedSize64 > uint64(maxSingleFileSize) {
			return fmt.Errorf("file %q exceeds max size (%d > %d)", f.Name, f.UncompressedSize64, maxSingleFileSize)
		}
		totalSize += int64(f.UncompressedSize64)
		if totalSize > maxTotalExtractSize {
			return fmt.Errorf("total extracted size exceeds limit (%d)", maxTotalExtractSize)
		}

		if err := extractFile(f, target); err != nil {
			return err
		}
	}

	return nil
}

func extractFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %q: %w", f.Name, err)
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode()&0755)
	if err != nil {
		return fmt.Errorf("create file %q: %w", target, err)
	}
	defer out.Close()

	n, err := io.Copy(out, io.LimitReader(rc, maxSingleFileSize+1))
	if err != nil {
		return fmt.Errorf("write file %q: %w", target, err)
	}
	if n > maxSingleFileSize {
		return fmt.Errorf("file %q exceeds max size on decompression (%d bytes)", f.Name, n)
	}

	return nil
}

// stripTopLevel removes a common top-level directory prefix from zip entries.
// Returns the original name if no common prefix exists.
func stripTopLevel(name string, files []*zip.File) string {
	if len(files) == 0 {
		return name
	}

	// Find common prefix.
	first := files[0].Name
	sep := strings.IndexByte(first, '/')
	if sep == -1 {
		return name
	}
	prefix := first[:sep+1]

	for _, f := range files {
		if !strings.HasPrefix(f.Name, prefix) {
			return name // no common prefix
		}
	}

	// All files share the prefix — strip it.
	return strings.TrimPrefix(name, strings.TrimSuffix(prefix, "/"))
}
