//go:build windows

package main

// dupFd is a no-op on Windows — fd-level redirection is not supported.
// stderr is still redirected at the Go level via os.Stderr = f.
func dupFd(_, _ int) error {
	return nil
}
