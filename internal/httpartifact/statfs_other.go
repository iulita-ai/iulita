//go:build !unix

package httpartifact

// diskAvailable is unavailable on non-Unix builds: reporting 0 makes quota
// reservation fail closed (disk reserve cannot be verified there).
func diskAvailable(string) (uint64, error) { return 0, errNoDiskStat }

var errNoDiskStat error = &diskStatError{}

type diskStatError struct{}

func (*diskStatError) Error() string { return "disk stats unavailable on this platform" }
