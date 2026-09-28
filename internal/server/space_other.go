//go:build !unix

package server

// availableBytes cannot be determined on this platform, so callers skip the
// pre-upload free space check and rely on the size limit alone.
func availableBytes(string) (uint64, bool) { return 0, false }

// ensureFreeSpace is a no-op where free space cannot be probed.
func ensureFreeSpace(string, int64) error { return nil }
