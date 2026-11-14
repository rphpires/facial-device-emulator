//go:build windows

package reachability

// Windows has no RLIMIT_NOFILE. Zero means "not applicable".
func maxOpenFiles() uint64 { return 0 }
