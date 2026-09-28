//go:build unix

package server

import (
	"fmt"
	"syscall"
)

// availableBytes reports the free space available to this process on the
// filesystem holding path. It returns ok=false when the answer cannot be
// determined, so callers can degrade instead of rejecting a valid upload.
func availableBytes(path string) (free uint64, ok bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, false
	}
	// Guard against overflow when the block size is not what we expect.
	blockSize := uint64(stat.Bsize)
	if blockSize == 0 {
		return 0, false
	}
	return stat.Bavail * blockSize, true
}

// ensureFreeSpace returns an error when the filesystem holding dir cannot
// plausibly hold need bytes. A small margin absorbs concurrent uploads and
// filesystem metadata.
func ensureFreeSpace(dir string, need int64) error {
	probe, ok := availableBytes(dir)
	if !ok {
		return nil
	}
	const margin = 16 << 20
	required := uint64(need) + margin
	if probe < required {
		return fmt.Errorf("need %d bytes including margin, %d available on %s", required, probe, dir)
	}
	return nil
}
