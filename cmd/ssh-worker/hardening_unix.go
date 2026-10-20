//go:build unix

package main

import "golang.org/x/sys/unix"

func disableCoreDumps() error {
	return unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
}
