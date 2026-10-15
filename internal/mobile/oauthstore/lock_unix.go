//go:build linux || darwin

package oauthstore

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// RealmLock holds exclusive ownership of a mobile realm directory.
type RealmLock struct{ file *os.File }

// LockRealm takes exclusive ownership of the realm directory.
func LockRealm(dir string) (*RealmLock, error) {
	if privateDir(dir) != nil {
		return nil, ErrKeys
	}
	path := filepath.Join(dir, "mobile-runtime.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600) //nolint:gosec // Path comes from explicit local runtime configuration, not remote input.
	if err != nil {
		return nil, ErrKeys
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		f.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, ErrKeys
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, ErrConflict
	}
	return &RealmLock{f}, nil
}

// Close releases the resources owned by this instance.
func (l *RealmLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN) //nolint:errcheck // Closing the descriptor also releases the lock.
	return l.file.Close()
}
