/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azurelustre

import (
	"errors"
	"fmt"
	"math"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
)

func getFilesystemUsage(path string) ([]*csi.VolumeUsage, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return nil, fmt.Errorf("statfs %q: %w", path, err)
	}
	return filesystemUsageFromStatfs(stats)
}

func filesystemUsageFromStatfs(stats unix.Statfs_t) ([]*csi.VolumeUsage, error) {
	if stats.Bsize <= 0 {
		return nil, fmt.Errorf("invalid filesystem block size %d", stats.Bsize)
	}
	if stats.Blocks > math.MaxInt64 || int64(stats.Blocks) > math.MaxInt64/stats.Bsize {
		return nil, errors.New("filesystem capacity exceeds CSI int64 range")
	}
	if stats.Bavail > math.MaxInt64 || int64(stats.Bavail) > math.MaxInt64/stats.Bsize {
		return nil, errors.New("filesystem available bytes exceed CSI int64 range")
	}
	if stats.Bfree > math.MaxInt64 || stats.Bfree > stats.Blocks {
		return nil, fmt.Errorf("filesystem free blocks %d exceed total blocks %d", stats.Bfree, stats.Blocks)
	}
	if stats.Files > math.MaxInt64 {
		return nil, errors.New("filesystem inode count exceeds CSI int64 range")
	}
	if stats.Ffree > math.MaxInt64 || stats.Ffree > stats.Files {
		return nil, fmt.Errorf("filesystem free inodes %d exceed total inodes %d", stats.Ffree, stats.Files)
	}

	return []*csi.VolumeUsage{
		{
			Unit:      csi.VolumeUsage_BYTES,
			Available: int64(stats.Bavail) * stats.Bsize,
			Total:     int64(stats.Blocks) * stats.Bsize,
			// Reserved blocks are free but unavailable; do not count them as used.
			Used: (int64(stats.Blocks) - int64(stats.Bfree)) * stats.Bsize,
		},
		{
			Unit:      csi.VolumeUsage_INODES,
			Available: int64(stats.Ffree),
			Total:     int64(stats.Files),
			Used:      int64(stats.Files - stats.Ffree),
		},
	}, nil
}
