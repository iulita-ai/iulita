package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClientSecretDirectorySyncPrecedesCredentialChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	secret := "new credential"
	directorySynced := false
	changed := false
	err := withDurableClientSecret(path, secret, func(directory string) error {
		if directory != filepath.Dir(path) {
			t.Fatal("wrong secret directory")
		}
		if syncErr := syncSecretDirectory(directory); syncErr != nil {
			return syncErr
		}
		directorySynced = true
		return nil
	}, func() error {
		if !directorySynced {
			t.Fatal("credential changed before its filename became durable")
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil || string(contents) != secret+"\n" {
			t.Fatal("credential changed before secret output completed", readErr)
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("secret output permissions", statErr)
		}
		changed = true
		return nil
	})
	if err != nil || !changed {
		t.Fatal("durable secret did not commit", err)
	}
}

func TestClientSecretSyncFailureKeepsPriorCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	failure := errors.New("injected directory sync failure")
	currentCredential := "old credential"
	err := withDurableClientSecret(path, "new credential", func(string) error {
		return failure
	}, func() error {
		currentCredential = "new credential"
		return nil
	})
	if !errors.Is(err, failure) || currentCredential != "old credential" {
		t.Fatal("failed directory durability changed credential", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed operation left a misleading secret output", err)
	}
}

func TestClientSecretFailureOnlyRemovesItsOwnNewFile(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-file", true: "existing-file"}[existing], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secret")
			if existing {
				if err := os.WriteFile(path, []byte("existing credential"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			changed := false
			err := withDurableClientSecret(path, "new credential", syncSecretDirectory, func() error {
				changed = true
				return errors.New("injected credential transaction failure")
			})
			if err == nil || changed == existing {
				t.Fatal("unexpected credential change on failure", err)
			}
			if existing {
				contents, readErr := os.ReadFile(path)
				if readErr != nil || string(contents) != "existing credential" {
					t.Fatal("failed operation changed an existing secret file", readErr)
				}
			} else if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatal("failed credential transaction left its new secret file", statErr)
			}
		})
	}
}
