package skillmgr

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func createTestZip(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	zipPath := filepath.Join(dir, "test.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return zipPath
}

func TestExtractZIP(t *testing.T) {
	dir := t.TempDir()
	zipPath := createTestZip(t, dir, map[string]string{
		"SKILL.md":  "---\nname: test\n---\nHello",
		"README.md": "Test skill",
	})

	destDir := filepath.Join(dir, "extracted")
	os.MkdirAll(destDir, 0755)

	if err := ExtractZIP(zipPath, destDir); err != nil {
		t.Fatal(err)
	}

	// Check files exist.
	if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err != nil {
		t.Error("SKILL.md not extracted")
	}
	if _, err := os.Stat(filepath.Join(destDir, "README.md")); err != nil {
		t.Error("README.md not extracted")
	}
}

func TestExtractZIPWithTopLevelDir(t *testing.T) {
	dir := t.TempDir()
	zipPath := createTestZip(t, dir, map[string]string{
		"skill-1.0.0/SKILL.md":  "---\nname: test\n---\nHello",
		"skill-1.0.0/README.md": "Test",
	})

	destDir := filepath.Join(dir, "extracted")
	os.MkdirAll(destDir, 0755)

	if err := ExtractZIP(zipPath, destDir); err != nil {
		t.Fatal(err)
	}

	// Files should be extracted without the top-level dir.
	if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err != nil {
		t.Error("SKILL.md not found (top-level strip failed)")
	}
}

func TestExtractZIPPathTraversal(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	fw, _ := w.Create("../../../etc/passwd")
	fw.Write([]byte("evil"))
	w.Close()
	f.Close()

	destDir := filepath.Join(dir, "extracted")
	os.MkdirAll(destDir, 0755)

	err = ExtractZIP(zipPath, destDir)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}
