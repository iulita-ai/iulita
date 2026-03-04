//go:build unix

package main

import "golang.org/x/sys/unix"

// dupFd duplicates oldfd onto newfd (equivalent to dup2).
// Uses x/sys/unix which handles platforms where only dup3 exists (e.g. linux/arm64).
func dupFd(oldfd, newfd int) error {
	return unix.Dup2(oldfd, newfd)
}
