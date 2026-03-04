package config

import (
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

const appName = "iulita"

// Paths holds resolved XDG-compliant paths for the application.
// On macOS: ~/Library/Application Support/iulita, ~/Library/Caches/iulita
// On Linux: ~/.config/iulita, ~/.local/share/iulita, ~/.cache/iulita, ~/.local/state/iulita
type Paths struct {
	ConfigDir string
	DataDir   string
	CacheDir  string
	StateDir  string
}

// ResolvePaths returns XDG-compliant paths, respecting IULITA_HOME override.
// If IULITA_HOME is set, all paths point to subdirectories under it.
func ResolvePaths() *Paths {
	if home := os.Getenv("IULITA_HOME"); home != "" {
		return &Paths{
			ConfigDir: home,
			DataDir:   filepath.Join(home, "data"),
			CacheDir:  filepath.Join(home, "cache"),
			StateDir:  filepath.Join(home, "state"),
		}
	}

	return &Paths{
		ConfigDir: filepath.Join(xdg.ConfigHome, appName),
		DataDir:   filepath.Join(xdg.DataHome, appName),
		CacheDir:  filepath.Join(xdg.CacheHome, appName),
		StateDir:  filepath.Join(xdg.StateHome, appName),
	}
}

// ConfigFile returns the path to config.toml.
func (p *Paths) ConfigFile() string {
	return filepath.Join(p.ConfigDir, "config.toml")
}

// DatabaseFile returns the default path to the SQLite database.
func (p *Paths) DatabaseFile() string {
	return filepath.Join(p.DataDir, "iulita.db")
}

// ModelsDir returns the directory for ONNX model files.
func (p *Paths) ModelsDir() string {
	return filepath.Join(p.DataDir, "models")
}

// SkillsDir returns the directory for external skill files.
func (p *Paths) SkillsDir() string {
	return filepath.Join(p.DataDir, "skills")
}

// ExternalSkillsDir returns the directory for downloaded/installed external skills.
func (p *Paths) ExternalSkillsDir() string {
	return filepath.Join(p.DataDir, "external-skills")
}

// LogFile returns the path to the log file.
func (p *Paths) LogFile() string {
	return filepath.Join(p.StateDir, "iulita.log")
}

// EncryptionKeyFile returns the path to the encryption key file (fallback).
func (p *Paths) EncryptionKeyFile() string {
	return filepath.Join(p.ConfigDir, "encryption.key")
}

// EnsureDirs creates all necessary directories with appropriate permissions.
func (p *Paths) EnsureDirs() error {
	dirs := []string{p.ConfigDir, p.DataDir, p.CacheDir, p.StateDir}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
	}
	return nil
}
