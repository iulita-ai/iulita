//go:build unix

package httpartifact

import "golang.org/x/sys/unix"

// diskAvailable reports free bytes visible to unprivileged users. A
// nonsensical stat reads as 0 (unknown) — quota reservation fails closed.
func diskAvailable(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	avail := st.Bavail
	block := uint64(st.Bsize)
	if block == 0 || avail == 0 || avail > (1<<62)/block {
		return 0, nil
	}
	return avail * block, nil
}
