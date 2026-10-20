package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteAccessWorkerEnvironmentOverrides(t *testing.T) {
	paths := testPaths(t)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[skills.remote_access]\nworker_url = 'https://old-worker.invalid:8443'\nworker_key_file = '/old/key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IULITA_SKILLS_REMOTE_ACCESS_WORKER_URL", "https://ssh-worker.internal:8443")
	t.Setenv("IULITA_SKILLS_REMOTE_ACCESS_WORKER_KEY_FILE", "/run/secrets/ssh-worker/key")
	cfg, k, loaded, err := Load(configPath, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded {
		t.Fatal("test config file was not loaded")
	}
	if cfg.Skills.RemoteAccess.WorkerURL != "https://ssh-worker.internal:8443" || cfg.Skills.RemoteAccess.WorkerKeyFile != "/run/secrets/ssh-worker/key" {
		t.Fatalf("environment overrides lost: %+v", cfg.Skills.RemoteAccess)
	}
	if k.Exists("skills.remote.access.worker.url") || k.Exists("skills.remote.access.worker.key.file") {
		t.Fatal("environment keys were split into unrelated config sections")
	}
}
