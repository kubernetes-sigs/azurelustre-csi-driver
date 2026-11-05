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
	"math"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFilesystemUsageFromStatfs(t *testing.T) {
	tests := []struct {
		name        string
		stats       unix.Statfs_t
		expected    []*csi.VolumeUsage
		expectedErr string
	}{
		{
			name:  "reserved blocks are not used space",
			stats: unix.Statfs_t{Bsize: 4096, Blocks: 1000, Bfree: 400, Bavail: 300, Files: 500, Ffree: 100},
			expected: []*csi.VolumeUsage{
				{Unit: csi.VolumeUsage_BYTES, Available: 1228800, Total: 4096000, Used: 2457600},
				{Unit: csi.VolumeUsage_INODES, Available: 100, Total: 500, Used: 400},
			},
		},
		{
			name:  "zero capacity and inode counts",
			stats: unix.Statfs_t{Bsize: 4096},
			expected: []*csi.VolumeUsage{
				{Unit: csi.VolumeUsage_BYTES},
				{Unit: csi.VolumeUsage_INODES},
			},
		},
		{
			name:  "full filesystem",
			stats: unix.Statfs_t{Bsize: 4096, Blocks: 1000, Files: 500},
			expected: []*csi.VolumeUsage{
				{Unit: csi.VolumeUsage_BYTES, Total: 4096000, Used: 4096000},
				{Unit: csi.VolumeUsage_INODES, Total: 500, Used: 500},
			},
		},
		{
			name:  "maximum signed capacity and inode counts",
			stats: unix.Statfs_t{Bsize: 1, Blocks: math.MaxInt64, Bfree: math.MaxInt64, Bavail: math.MaxInt64, Files: math.MaxInt64, Ffree: math.MaxInt64},
			expected: []*csi.VolumeUsage{
				{Unit: csi.VolumeUsage_BYTES, Available: math.MaxInt64, Total: math.MaxInt64},
				{Unit: csi.VolumeUsage_INODES, Available: math.MaxInt64, Total: math.MaxInt64},
			},
		},
		{
			name:  "largest whole block capacity below signed limit",
			stats: unix.Statfs_t{Bsize: 4096, Blocks: math.MaxInt64 / 4096},
			expected: []*csi.VolumeUsage{
				{Unit: csi.VolumeUsage_BYTES, Total: math.MaxInt64 / 4096 * 4096, Used: math.MaxInt64 / 4096 * 4096},
				{Unit: csi.VolumeUsage_INODES},
			},
		},
		{
			name:        "zero block size",
			stats:       unix.Statfs_t{},
			expectedErr: "invalid filesystem block size 0",
		},
		{
			name:        "negative block size",
			stats:       unix.Statfs_t{Bsize: -1},
			expectedErr: "invalid filesystem block size -1",
		},
		{
			name:        "capacity exceeds signed range",
			stats:       unix.Statfs_t{Bsize: 1, Blocks: uint64(math.MaxInt64) + 1},
			expectedErr: "filesystem capacity exceeds CSI int64 range",
		},
		{
			name:        "capacity multiplication exceeds signed range",
			stats:       unix.Statfs_t{Bsize: 4096, Blocks: math.MaxInt64/4096 + 1},
			expectedErr: "filesystem capacity exceeds CSI int64 range",
		},
		{
			name:        "capacity multiplication would wrap unsigned range",
			stats:       unix.Statfs_t{Bsize: 4096, Blocks: math.MaxUint64},
			expectedErr: "filesystem capacity exceeds CSI int64 range",
		},
		{
			name:        "available bytes exceed signed range",
			stats:       unix.Statfs_t{Bsize: 1, Bavail: uint64(math.MaxInt64) + 1},
			expectedErr: "filesystem available bytes exceed CSI int64 range",
		},
		{
			name:        "available bytes multiplication exceeds signed range",
			stats:       unix.Statfs_t{Bsize: 4096, Bavail: math.MaxInt64/4096 + 1},
			expectedErr: "filesystem available bytes exceed CSI int64 range",
		},
		{
			name:        "free blocks exceed total",
			stats:       unix.Statfs_t{Bsize: 4096, Blocks: 1, Bfree: 2},
			expectedErr: "filesystem free blocks 2 exceed total blocks 1",
		},
		{
			name:        "free blocks exceed signed range",
			stats:       unix.Statfs_t{Bsize: 1, Blocks: math.MaxInt64, Bfree: uint64(math.MaxInt64) + 1},
			expectedErr: "filesystem free blocks 9223372036854775808 exceed total blocks 9223372036854775807",
		},
		{
			name:        "inode count exceeds signed range",
			stats:       unix.Statfs_t{Bsize: 4096, Files: uint64(math.MaxInt64) + 1},
			expectedErr: "filesystem inode count exceeds CSI int64 range",
		},
		{
			name:        "free inodes exceed total",
			stats:       unix.Statfs_t{Bsize: 4096, Files: 1, Ffree: 2},
			expectedErr: "filesystem free inodes 2 exceed total inodes 1",
		},
		{
			name:        "free inodes exceed signed range",
			stats:       unix.Statfs_t{Bsize: 4096, Files: math.MaxInt64, Ffree: uint64(math.MaxInt64) + 1},
			expectedErr: "filesystem free inodes 9223372036854775808 exceed total inodes 9223372036854775807",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := filesystemUsageFromStatfs(test.stats)
			if test.expectedErr != "" {
				require.EqualError(t, err, test.expectedErr)
				assert.Nil(t, actual)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.expected, actual)
		})
	}
}
